package kube

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// Connect는 KUBECONFIG(없으면 ~/.kube/config)로, 그것도 없으면 클러스터 안의 서비스 계정으로 연결한다.
func Connect() (kubernetes.Interface, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube: load config: %w", err)
	}
	cfg.UserAgent = "gpu-pool-collector"
	kc, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube: client: %w", err)
	}
	return kc, nil
}

// Watcher는 Node·Pod를 informer로 받아 두고, 바뀔 때마다 Changes로 알린다.
type Watcher struct {
	factory informers.SharedInformerFactory
	nodes   listerscorev1.NodeLister
	pods    listerscorev1.PodLister
	synced  []cache.InformerSynced
	changes chan struct{}
}

// NewWatcher는 Watcher를 만든다. resync는 informer가 캐시 전체를 다시 알리는 주기다.
func NewWatcher(kc kubernetes.Interface, resync time.Duration) (*Watcher, error) {
	w := &Watcher{changes: make(chan struct{}, 1)}
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { w.notify() },
		UpdateFunc: func(any, any) { w.notify() },
		DeleteFunc: func(any) { w.notify() },
	}

	w.factory = informers.NewSharedInformerFactory(kc, resync)
	nodeInf := w.factory.Core().V1().Nodes()
	podInf := w.factory.Core().V1().Pods()
	w.nodes, w.pods = nodeInf.Lister(), podInf.Lister()
	for _, inf := range []cache.SharedIndexInformer{nodeInf.Informer(), podInf.Informer()} {
		if _, err := inf.AddEventHandler(handler); err != nil {
			return nil, fmt.Errorf("kube: add handler: %w", err)
		}
		w.synced = append(w.synced, inf.HasSynced)
	}
	return w, nil
}

// Start는 informer를 띄우고 처음 목록을 다 받을 때까지 최대 syncTimeout만큼 기다린다.
// ctx가 끝나면 informer도 멈춘다.
func (w *Watcher) Start(ctx context.Context, syncTimeout time.Duration) error {
	w.factory.Start(ctx.Done())
	waitCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	if !cache.WaitForCacheSync(waitCtx.Done(), w.synced...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("kube: informer caches did not sync within %s", syncTimeout)
	}
	return nil
}

// Changes는 Node·Pod가 바뀌면 신호를 보낸다. 신호가 밀려 있으면 하나로 합친다.
func (w *Watcher) Changes() <-chan struct{} {
	return w.changes
}

func (w *Watcher) notify() {
	select {
	case w.changes <- struct{}{}:
	default:
	}
}

// Snapshot은 informer 캐시로 지금의 Snapshot을 만든다. Start가 끝난 뒤에 부른다.
func (w *Watcher) Snapshot() (Snapshot, error) {
	nodes, err := w.nodes.List(labels.Everything())
	if err != nil {
		return Snapshot{}, fmt.Errorf("kube: list nodes: %w", err)
	}
	pods, err := w.pods.List(labels.Everything())
	if err != nil {
		return Snapshot{}, fmt.Errorf("kube: list pods: %w", err)
	}
	return Build(nodes, pods), nil
}
