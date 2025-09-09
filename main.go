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
	// Иначе — локальный kubeconfig (~/.kube/config)
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
		stuck, reason := isStuckInContainerCreating(&pod, pendingTimeout)
		if stuck {
			if firstSeen, ok := podProblemTimes[pod.Name]; !ok {
				// первая фиксация проблемы — запоминаем время
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
				// Логирование
				log.Printf("Problem pod details: %+v", ps)

				// Дергаем ручку(пока заглушка)
				if err := notifyAPI(ctx, pod.Namespace, pod.Name, reason); err != nil {
					log.Printf("notify error for pod %s: %v", pod.Name, err)
				} else {
					log.Printf("notify OK for pod %s", pod.Name)
				}
				// сбрасываем таймер
				delete(podProblemTimes, pod.Name)
			}
		} else {
			// если починился — удаляем из карты
			delete(podProblemTimes, pod.Name)
		}
	}
}

func isStuckInContainerCreating(pod *corev1.Pod, timeout time.Duration) (bool, string) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" {
			if time.Since(pod.CreationTimestamp.Time) > timeout {
				return true, "ContainerCreating"
			}
		}
	}
	return false, ""
}

// notifyAPI — заглушка: здесь позже дергаем ваш внешний API.
// Сейчас просто логируем и ждём ~200мс, имитируя сетевой вызов.
func notifyAPI(ctx context.Context, ns, podName, reason string) error {
	log.Printf("[MOCK] notify external API: namespace=%s pod=%s reason=%s", ns, podName, reason)
	select {
	case <-time.After(200 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
