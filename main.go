package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type PodStatus struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Namespace string    `json:"namespace"`
	Duration  string    `json:"duration"`
	Timestamp time.Time `json:"timestamp"`
}

var (
	namespace       = getEnv("NAMESPACE", "default")
	labelSelector   = os.Getenv("LABEL_SELECTOR") // опционально: например "app=k8s-watchdog"
	pendingTimeout  = getPendingTimeout()
	checkInterval   = getCheckInterval()
	logFile         *os.File
	podProblemTimes = make(map[string]time.Time)
)

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getPendingTimeout() time.Duration {
	str := getEnv("PENDING_TIMEOUT", "5") // минуты
	mins, err := strconv.Atoi(str)
	if err != nil || mins <= 0 {
		log.Printf("Invalid PENDING_TIMEOUT=%q, fallback to 5m", str)
		return 5 * time.Minute
	}
	return time.Duration(mins) * time.Minute
}

func getCheckInterval() time.Duration {
	str := getEnv("CHECK_INTERVAL", "30") // секунды
	secs, err := strconv.Atoi(str)
	if err != nil || secs <= 0 {
		log.Printf("Invalid CHECK_INTERVAL=%q, fallback to 30s", str)
		return 30 * time.Second
	}
	return time.Duration(secs) * time.Second
}

func setupLogging() {
	var err error
	logFile, err = os.OpenFile("watchdog.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("open log file error: %v", err)
	}
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func kubeConfig() *rest.Config {
	// Сначала пробуем in-cluster
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg
	}
	// Фолбэк: локальный kubeconfig (~/.kube/config)
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("create kubeconfig error: %v", err)
	}
	return cfg
}

func main() {
	setupLogging()
	defer logFile.Close()

	log.Printf("Watchdog: namespace=%s, labelSelector=%q, timeout=%s, interval=%s",
		namespace, labelSelector, pendingTimeout, checkInterval)

	cfg := kubeConfig()
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("create clientset error: %v", err)
	}

	t := time.NewTicker(checkInterval)
	defer t.Stop()

	for {
		checkPods(clientset)
		<-t.C
	}
}

func checkPods(clientset *kubernetes.Clientset) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	listOpts := metav1.ListOptions{}
	if labelSelector != "" {
		listOpts.LabelSelector = labelSelector
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, listOpts)
	if err != nil {
		log.Printf("list pods error: %v", err)
		return
	}

	now := time.Now()
	for _, pod := range pods.Items {
		should, reason := shouldRestartPod(&pod, pendingTimeout)
		if should {
			if firstSeen, ok := podProblemTimes[pod.Name]; !ok {
				podProblemTimes[pod.Name] = now
				log.Printf("Problem detected: %s (%s) — starting timer", pod.Name, reason)
			} else if now.Sub(firstSeen) >= pendingTimeout {
				log.Printf("Restarting pod %s (stuck %s, reason=%s)", pod.Name, now.Sub(firstSeen), reason)

				ps := PodStatus{
					Name:      pod.Name,
					Status:    string(pod.Status.Phase),
					Namespace: pod.Namespace,
					Duration:  fmt.Sprintf("%.0f seconds", now.Sub(firstSeen).Seconds()),
					Timestamp: now,
				}
				log.Printf("Problem pod details: %+v", ps)

				if err := restartPod(ctx, clientset, pod.Namespace, pod.Name); err != nil {
					log.Printf("restart error for pod %s: %v", pod.Name, err)
				} else {
					log.Printf("restart OK for pod %s", pod.Name)
				}
				delete(podProblemTimes, pod.Name)
			}
		} else {
			delete(podProblemTimes, pod.Name)
		}
	}
}

func shouldRestartPod(pod *corev1.Pod, timeout time.Duration) (bool, string) {
	// Быстрые отказы по фазам
	switch pod.Status.Phase {
	case corev1.PodPending, corev1.PodRunning:
		// ok, дальше смотри контейнеры
	default:
		// Succeeded/Failed/Unknown — на усмотрение. Обычно не трогаем.
		return false, ""
	}

	reasonsToWatch := map[string]bool{
		"ContainerCreating":          true,
		"ErrImagePull":               true,
		"ImagePullBackOff":           true,
		"CrashLoopBackOff":           true,
		"CreateContainerConfigError": true,
	}

	for _, cs := range pod.Status.ContainerStatuses {
		// Waiting
		if cs.State.Waiting != nil {
			r := cs.State.Waiting.Reason
			if reasonsToWatch[r] {
				// «Сколько висим?» — от времени создания пода
				if time.Since(pod.CreationTimestamp.Time) > timeout {
					return true, r
				}
			}
		}
		// Running but not Ready долго — тоже подозрительно
		if cs.State.Running != nil && !cs.Ready {
			startedAt := cs.State.Running.StartedAt.Time
			if time.Since(startedAt) > timeout {
				return true, "RunningNotReady"
			}
		}
	}
	return false, ""
}

func restartPod(ctx context.Context, clientset *kubernetes.Clientset, ns, podName string) error {
	// Самый простой и надёжный «рестарт» — удалить под.
	// Если он под управлением контроллера (Deployment/RS/…),
	// Kubernetes пересоздаст новый под.
	grace := int64(0)
	propagation := metav1.DeletePropagationForeground
	return clientset.CoreV1().Pods(ns).Delete(ctx, podName, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
		PropagationPolicy:  &propagation,
	})
}
