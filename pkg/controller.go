package pkg

import (
	"context"
	coreV1 "k8s.io/api/core/v1"
	networkingV1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	informerCoreV1 "k8s.io/client-go/informers/core/v1"
	informernetworkingv1 "k8s.io/client-go/informers/networking/v1"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/listers/core/v1"
	networkingv1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"reflect"
	"time"
)

const (
	workNum  = 5
	maxRetry = 10
)

type controller struct {
	client        kubernetes.Interface
	ingressLister networkingv1.IngressLister
	serviceLister corev1.ServiceLister
	queue         workqueue.RateLimitingInterface
}

func (c *controller) Run(stopChan chan struct{}) {
	for i := 0; i < workNum; i++ {
		go wait.Until(c.worker, time.Minute, stopChan)
	}
	<-stopChan
}

func NewController(client kubernetes.Interface, serviceInformer informerCoreV1.ServiceInformer, ingressInformer informernetworkingv1.IngressInformer) controller {
	c := controller{
		client:        client,
		serviceLister: serviceInformer.Lister(),
		ingressLister: ingressInformer.Lister(),
		queue:         workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{}),
	}

	serviceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addService,
		UpdateFunc: c.updateService,
	})

	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: c.deleteIngress,
	})

	return c
}

func (c *controller) enqueue(obj interface{}) {
	//c.queue.Add(obj)
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
	}

	c.queue.Add(key)
}

// 当service有annotation为"ingress/http:true"时，创建ingress
// 没有则忽略
func (c *controller) addService(obj interface{}) {
	c.enqueue(obj)
}

// 当service包含指定的annotation，检查资源是否存在，不存在就创建ingress，存在则忽略
// 不包含指定的annotation，检查ingress对象是否存在，存在则删除，不存在则忽略
func (c *controller) updateService(oldObj, newObj interface{}) {
	// todo 比较annotation
	_, ok := newObj.(*coreV1.Service)
	if !ok {
		return
	}

	if reflect.DeepEqual(oldObj, newObj) {
		return
	}
	c.enqueue(newObj)
}

func (c *controller) deleteIngress(obj interface{}) {
	ingress := obj.(*networkingV1.Ingress)
	ownerReference := v1.GetControllerOf(ingress)
	if ownerReference == nil {
		return
	}

	if ownerReference.Kind != "Service" {
		return
	}

	c.queue.Add(ingress.Namespace + "/" + ingress.Name)
}

func (c *controller) worker() {
	for c.processNextItem() {
	}
}

func (c *controller) processNextItem() bool {
	item, shutDown := c.queue.Get()
	if shutDown {
		return false
	}

	defer c.queue.Done(item)

	key := item.(string)

	err := c.syncService(key)
	if err != nil {
		c.handlerError(key, err)
		return false
	}
	return true
}

func (c *controller) syncService(key string) (err error) {
	nameSpaceKey, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return
	}

	// 删除
	service, err := c.serviceLister.Services(nameSpaceKey).Get(name)
	if errors.IsNotFound(err) {
		return nil
	}

	// 新增和删除
	_, ok := service.GetAnnotations()["ingress/http"]
	ingress, err := c.ingressLister.Ingresses(nameSpaceKey).Get(name)
	if err != nil && !errors.IsNotFound(err) {
		return
	}

	if ok && errors.IsNotFound(err) {
		// create ingress
		ig := c.constructIngress(service)
		_, err = c.client.NetworkingV1().Ingresses(nameSpaceKey).Create(context.TODO(), ig, v1.CreateOptions{})
		if err != nil {
			return
		}
	} else if !ok && ingress != nil {
		// delete ingress
		err = c.client.NetworkingV1().Ingresses(nameSpaceKey).Delete(context.TODO(), name, v1.DeleteOptions{})
		if err != nil {
			return
		}
	}
	return nil
}

func (c *controller) handlerError(key string, err error) {
	if c.queue.NumRequeues(key) <= maxRetry {
		c.queue.AddRateLimited(key)
	}

	runtime.HandleError(err)
	c.queue.Forget(key)
}

func (c *controller) constructIngress(service *coreV1.Service) *networkingV1.Ingress {
	ig := networkingV1.Ingress{}
	ig.Name = service.Name
	ig.Namespace = service.Namespace
	ig.ObjectMeta.OwnerReferences = []v1.OwnerReference{
		*v1.NewControllerRef(service, coreV1.SchemeGroupVersion.WithKind("Service")),
	}
	pathType := networkingV1.PathTypePrefix
	ig.Spec = networkingV1.IngressSpec{
		Rules: []networkingV1.IngressRule{
			{
				Host: "example.com",
				IngressRuleValue: networkingV1.IngressRuleValue{
					HTTP: &networkingV1.HTTPIngressRuleValue{
						Paths: []networkingV1.HTTPIngressPath{
							{
								Path:     "/",
								PathType: &pathType,
								Backend: networkingV1.IngressBackend{
									Service: &networkingV1.IngressServiceBackend{
										Name: service.Name,
										Port: networkingV1.ServiceBackendPort{
											Number: 80,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return &ig
}
