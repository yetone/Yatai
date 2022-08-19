package controllersv1

import (
	"sort"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-schemas/schemasv1"
	"github.com/bentoml/yatai/api-server/services"
)

type kubeController struct {
	baseController
}

var KubeController = kubeController{}

func (c *kubeController) GetPodKubeEvents(ctx *gin.Context, schema *GetClusterSchema) error {
	var err error

	ctx.Request.Header.Del("Origin")
	conn, err := wsUpgrader.Upgrade(ctx.Writer, ctx.Request, nil)
	if err != nil {
		logrus.Errorf("ws connect failed: %q", err.Error())
		return err
	}
	defer conn.Close()

	defer func() {
		writeWsError(conn, err)
	}()

	cluster, err := schema.GetCluster(ctx)
	if err != nil {
		return err
	}

	err = ClusterController.canView(ctx, cluster)
	if err != nil {
		return err
	}

	closeCh := make(chan struct{})
	toClose := make(chan struct{}, 1)

	go func() {
		select {
		case <-ctx.Done():
			close(closeCh)
		case <-toClose:
			close(closeCh)
		}
	}()

	doClose := func() {
		select {
		case toClose <- struct{}{}:
		default:
		}
	}
	defer doClose()

	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logrus.Errorf("ws read failed: %q", err.Error())
				}
				doClose()
				return
			}
		}
	}()

	filter := func(event *corev1.Event) bool {
		return true
	}

	kubeNs := ctx.Query("namespace")
	podName := ctx.Query("pod_name")
	if podName != "" {
		var cliset *kubernetes.Clientset
		cliset, _, err = services.ClusterService.GetKubeCliSet(ctx, cluster)
		if err != nil {
			return err
		}

		podsCli := cliset.CoreV1().Pods(kubeNs)

		var pod *corev1.Pod
		pod, err = podsCli.Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		filter = func(event *corev1.Event) bool {
			return event.InvolvedObject.Kind == consts.KubeEventResourceKindPod && event.InvolvedObject.UID == pod.UID
		}
	}

	eventInformer, eventLister, err := services.GetEventInformer(ctx, cluster, kubeNs)
	if err != nil {
		err = errors.Wrap(err, "get event informer")
		return err
	}

	informer := eventInformer.Informer()
	defer runtime.HandleCrash()

	ticker := time.NewTicker(time.Second * 3)
	defer ticker.Stop()

	var failedCount int32 = 0
	var maxFailed int32 = 10

	failed := func() {
		atomic.AddInt32(&failedCount, 1)
		time.Sleep(time.Second * 10)
	}

	send := func() {
		select {
		case <-closeCh:
			return
		default:
		}

		var err error
		defer func() {
			writeWsError(conn, err)
			if err != nil {
				failed()
			}
		}()

		var events []*corev1.Event
		events, err = eventLister.List(labels.Everything())
		if err != nil {
			err = errors.Wrap(err, "list events")
			return
		}

		_events := make([]*corev1.Event, 0)

		for _, event := range events {
			if !filter(event) {
				continue
			}
			_events = append(_events, event)
		}

		sort.SliceStable(_events, func(i, j int) bool {
			it := _events[i].LastTimestamp
			jt := _events[j].LastTimestamp

			return it.Before(&jt)
		})

		err = conn.WriteJSON(&schemasv1.WsRespSchema{
			Type:    schemasv1.WsRespTypeSuccess,
			Message: "",
			Payload: _events,
		})
		if err != nil {
			err = errors.Wrap(err, "ws write json")
			return
		}
	}

	send()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if event, ok := obj.(*corev1.Event); ok {
				if !filter(event) {
					return
				}
				send()
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if event, ok := newObj.(*corev1.Event); ok {
				if !filter(event) {
					return
				}
				send()
			}
		},
		DeleteFunc: func(obj interface{}) {
			if event, ok := obj.(*corev1.Event); ok {
				if !filter(event) {
					return
				}
				send()
			}
		},
	})

	func() {
		ticker := time.NewTicker(time.Second * 10)
		defer ticker.Stop()

		for {
			select {
			case <-closeCh:
				return
			default:
			}

			if failedCount > maxFailed {
				err = errors.New("ws events failed too frequently!")
				return
			}

			<-ticker.C
		}
	}()

	return nil
}

func (c *kubeController) GetDeploymentKubeEvents(ctx *gin.Context, schema *GetDeploymentSchema) error {
	var err error

	ctx.Request.Header.Del("Origin")
	conn, err := wsUpgrader.Upgrade(ctx.Writer, ctx.Request, nil)
	if err != nil {
		logrus.Errorf("ws connect failed: %q", err.Error())
		return err
	}
	defer conn.Close()

	defer func() {
		writeWsError(conn, err)
	}()

	deployment, err := schema.GetDeployment(ctx)
	if err != nil {
		return err
	}

	err = DeploymentController.canView(ctx, deployment)
	if err != nil {
		return err
	}

	closeCh := make(chan struct{})
	toClose := make(chan struct{}, 1)

	go func() {
		select {
		case <-ctx.Done():
			close(closeCh)
		case <-toClose:
			close(closeCh)
		}
	}()

	doClose := func() {
		select {
		case toClose <- struct{}{}:
		default:
		}
	}
	defer doClose()

	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logrus.Errorf("ws read failed: %q", err.Error())
				}
				doClose()
				return
			}
		}
	}()

	cluster, err := schema.GetCluster(ctx)
	if err != nil {
		return err
	}

	eventFilter, err := services.KubeEventService.MakeDeploymentKubeEventFilter(ctx, deployment, nil)
	if err != nil {
		return err
	}

	podName := ctx.Query("pod_name")
	if podName != "" {
		var cliset *kubernetes.Clientset
		cliset, _, err = services.ClusterService.GetKubeCliSet(ctx, cluster)
		if err != nil {
			return err
		}

		kubeNs := services.DeploymentService.GetKubeNamespace(deployment)
		podsCli := cliset.CoreV1().Pods(kubeNs)

		var pod *corev1.Pod
		pod, err = podsCli.Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if pod.Labels[consts.KubeLabelYataiDeployment] != deployment.Name {
			err = errors.Errorf("pod %s not in this deployment", podName)
			return err
		}

		eventFilter = func(event *corev1.Event) bool {
			return event.InvolvedObject.Kind == consts.KubeEventResourceKindPod && event.InvolvedObject.UID == pod.UID
		}
	}

	eventInformer, eventLister, err := services.GetEventInformer(ctx, cluster, services.DeploymentService.GetKubeNamespace(deployment))
	if err != nil {
		err = errors.Wrap(err, "get event informer")
		return err
	}

	eventInformer_ := eventInformer.Informer()
	defer runtime.HandleCrash()

	ticker := time.NewTicker(time.Second * 3)
	defer ticker.Stop()

	var failedCount int32 = 0
	var maxFailed int32 = 10

	failed := func() {
		atomic.AddInt32(&failedCount, 1)
		time.Sleep(time.Second * 10)
	}

	seen := make(map[string]struct{})

	send := func() {
		select {
		case <-closeCh:
			return
		default:
		}

		var err error
		defer func() {
			writeWsError(conn, err)
			if err != nil {
				failed()
			}
		}()

		var events []*corev1.Event

		events, err = eventLister.List(labels.Everything())
		if err != nil {
			err = errors.Wrap(err, "list events")
			return
		}

		_events := make([]*corev1.Event, 0)

		for _, event := range events {
			if !eventFilter(event) {
				continue
			}
			if _, ok := seen[event.Message]; ok {
				continue
			}
			_events = append(_events, event)
		}

		sort.SliceStable(_events, func(i, j int) bool {
			ie := _events[i]
			je := _events[j]

			it := time.Now()
			// nolint: gocritic
			if !ie.EventTime.IsZero() {
				it = ie.EventTime.Time
			} else if !ie.LastTimestamp.IsZero() {
				it = ie.LastTimestamp.Time
			} else if !ie.FirstTimestamp.IsZero() {
				it = ie.FirstTimestamp.Time
			}

			jt := time.Now()
			// nolint: gocritic
			if !je.EventTime.IsZero() {
				jt = je.EventTime.Time
			} else if !je.LastTimestamp.IsZero() {
				jt = je.LastTimestamp.Time
			} else if !je.FirstTimestamp.IsZero() {
				jt = je.FirstTimestamp.Time
			}

			return it.Before(jt)
		})

		select {
		case <-closeCh:
			return
		default:
		}

		err = conn.WriteJSON(&schemasv1.WsRespSchema{
			Type:    schemasv1.WsRespTypeSuccess,
			Message: "",
			Payload: _events,
		})
	}

	send()

	eventInformer_.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if event, ok := obj.(*corev1.Event); ok {
				if !eventFilter(event) {
					return
				}
				send()
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if event, ok := newObj.(*corev1.Event); ok {
				if !eventFilter(event) {
					return
				}
				send()
			}
		},
		DeleteFunc: func(obj interface{}) {
			if event, ok := obj.(*corev1.Event); ok {
				if !eventFilter(event) {
					return
				}
				send()
			}
		},
	})

	func() {
		ticker := time.NewTicker(time.Second * 10)
		defer ticker.Stop()

		for {
			select {
			case <-closeCh:
				return
			default:
			}

			if failedCount > maxFailed {
				err = errors.New("ws events failed too frequently!")
				return
			}

			<-ticker.C
		}
	}()

	return nil
}
