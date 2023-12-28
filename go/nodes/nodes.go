package nodes

import (
	"context"
	"fmt"
	"github.com/openark/golib/log"
	policyv1 "k8s.io/api/policy/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"os"
	k8sconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)
var ClientSet *kubernetes.Clientset
var NodeMap = make(map [string] *corev1.Node)

func init()  {
	// Get a config to talk to the apiserver
	cfg, err := k8sconfig.GetConfig()
	if err != nil {
		log.Error("unable to get configuration", err)
		os.Exit(1)
	}

	// 创建Kubernetes客户端集群
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error("Error creating clientset:%v\n", err)
		os.Exit(1)
	}

	ClientSet = clientset

}

func NodeListWatch()  {

	nodes, err := ClientSet.CoreV1().Nodes().List(context.Background(),metav1.ListOptions{})
	if err != nil {
		log.Error("Error listing nodes: %v", err)
		return
	}
	for _, node := range nodes.Items {
		NodeMap[node.Name] = &node
	}

	// 创建一个节点缓存器，用于缓存节点信息
	nodeListWatcher := cache.NewListWatchFromClient(
		ClientSet.CoreV1().RESTClient(),
		"nodes",
		metav1.NamespaceAll,
		fields.Everything(),
	)
	_, controller := cache.NewInformer(
		nodeListWatcher,
		&corev1.Node{},
		0,
		cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, newObj interface{}) {
				node := newObj.(*corev1.Node)
				log.Info("node update", node.Name, node.Status.Conditions)
				NodeMap[node.Name] = node

			},
		})

	stopCh := make(chan struct{})
	//defer close(stopCh)

	// 启动节点缓存控制器
	go controller.Run(stopCh)
}

func IsNodeReady(node *corev1.Node) bool {
	// 检查节点状态是否处于Ready状态
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
// 判断mysql pod所在的节点是否是正常的, 不正常则需要漂移
func IsServerDrift(ip string) (bool, *corev1.Pod) {
	podList := &corev1.PodList{}

	label := "app.kubernetes.io/managed-by=mysql.presslabs.org"

	option := metav1.ListOptions{
		LabelSelector: label,
	}

	podList, err := ClientSet.CoreV1().Pods("").List(context.Background(),option)
	if err != nil {
		log.Error("Error listing pods: %v", err)
		return false, nil
	}

	for _, pod := range podList.Items {
		if node, ok := NodeMap[pod.Spec.NodeName]; ok && !IsNodeReady(node) {
			log.Infof("%s is not ready, pod %s/%s is drift", node,  pod.Namespace, pod.Name)
			return true, &pod
		}

	}

	return false, nil
}

func GetMasterPod() (bool, *corev1.Pod) {
	podList := &corev1.PodList{}

	label := "app.kubernetes.io/managed-by=mysql.presslabs.org,role=master"

	option := metav1.ListOptions{
		LabelSelector: label,
	}

	podList, err := ClientSet.CoreV1().Pods("").List(context.Background(),option)
	if err != nil {
		log.Error("Error listing pods: %v", err)
		return false, nil
	}
	log.Infof("GetMasterPod: pod list len %d", len(podList.Items))

	for _, pod := range podList.Items {
		if pod.Labels["role"] == "master" {
			log.Infof("%s is no healty, pod %s/%s is drift", pod.Name, pod.Namespace, pod.Name)
			return true, &pod
		}

	}

	return false, nil
}
func GetOrcPod() (bool, *corev1.Pod) {
	podList := &corev1.PodList{}

	label := "app=mysql-operator,release=mysql-operator"

	option := metav1.ListOptions{
		LabelSelector: label,
	}

	podList, err := ClientSet.CoreV1().Pods("").List(context.Background(),option)
	if err != nil {
		log.Error("Error listing pods: %v", err)
		return false, nil
	}
	log.Infof("GetOrcPod: pod list len %d", len(podList.Items))

	for _, pod := range podList.Items {
		if node, ok := NodeMap[pod.Spec.NodeName]; ok && !IsNodeReady(node) {
			log.Infof("%s is not ready, pod %s/%s is drift", node,  pod.Namespace, pod.Name)
			return true, &pod
		}

	}

	return false, nil
}
func RemovePod(pod *corev1.Pod) {
	log.Infof("pod %s/%s is server drift start", pod.Namespace, pod.Name)
	gracePeriodSeconds := int64(0)
	deletePolicy := metav1.DeletePropagationForeground

	evict := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: pod.Namespace,
			Name: pod.Name,
		},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: &gracePeriodSeconds,
			PropagationPolicy: &deletePolicy,
		},
	}

	err := ClientSet.CoreV1().Pods(pod.Namespace).EvictV1(context.Background(), evict)
	if err != nil {
		log.Errorf(fmt.Sprintf("failed to evict pod %s/%s", pod.Namespace, pod.Name), err)

	} else {
		log.Info(fmt.Sprintf("success to evict pod %s/%s", pod.Namespace, pod.Name))
	}

	// 设置删除期限为零，即立即终止 Pod
	deleteOptions := metav1.DeleteOptions{
		GracePeriodSeconds: new(int64),
	}

	err = ClientSet.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, deleteOptions)
	if err != nil {
		log.Errorf(fmt.Sprintf("Failed to delete pod %s/%s", pod.Namespace, pod.Name), err)
	}
}