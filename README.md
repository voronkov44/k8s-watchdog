# ☸️🐶 Kubernetes Watchdog

Простой watchdog на Go для мониторинга подов в Kubernetes.  
Если под застрял в статусе `ContainerCreating`, `ImagePullBackOff`, `CrashLoopBackOff` и т. п. дольше заданного таймаута — watchdog удаляет под, чтобы контроллер пересоздал его.

---

## 🚀 Сборка и запуск

### 1. Собрать Docker-образ
Подключаемся к докеру внутри Minikube:
```bash
eval $(minikube docker-env)
```

Билдим образ:
```bash
docker build -t watchdog:local .
```

### 2. Применить манифесты
Создаём права доступа (RBAC):
```bash
kubectl apply -f deploy/rbac.yaml
```

Деплой приложения:
```bash
kubectl apply -f deploy/deployment.yaml
```

### 3. Проверить логи
```bash
kubectl logs -f deploy/watchdog
```

### 4. Обновить после пересборки
```bash
docker build -t watchdog:local .
kubectl rollout restart deployment watchdog
```

## ⚙️ Переменные окружения

Можно задавать через deployment.yaml → env:

NAMESPACE — namespace для мониторинга (по умолчанию default).

LABEL_SELECTOR — селектор, например app=my-service (опционально).

PENDING_TIMEOUT — сколько минут ждать перед рестартом (дефолт 5).

CHECK_INTERVAL — интервал проверки в секундах (дефолт 30).

## 🛠️ Полезные команды
Посмотреть поды:
```bash
kubectl get pods
```

Перезапустить деплой вручную:
```bash
kubectl rollout restart deployment watchdog
```

Удалить всё:
```bash
kubectl delete -f deploy/deployment.yaml
kubectl delete -f deploy/rbac.yaml
```


## 📂 Структура проекта
```
k8s-watchdog/
├── .env
├── .gitignore
├── Dockerfile
├── go.mod
├── go.sum
├── main.go
└── deploy/
    ├── rbac.yaml
    └── deployment.yaml
```





