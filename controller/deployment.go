package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"strconv"
	"time"
	"net/http"
	"github.com/gorilla/mux"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

type Container struct{
	Name string
	Image string
	Envs []string
}

type deployments struct {
	ID []string
}

type DeploymentEvent struct{
	Name string
	Namespace string
	Containers []Container
	ReplicaCurrent string
	ReplicaDesired string
	Age string
}

func GetDeployment(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Inside GetDeployment func\n")
	vars := mux.Vars(r)
	ns := vars["ns"]
	dep := vars["dep"]
	fmt.Printf("dep %s in $s \n", dep, ns)
	kubeclient := GetKubeClient()
	newDepl, err := kubeclient.AppsV1().Deployments(mux.Vars(r)["ns"]).Get(mux.Vars(r)["dep"],metav1.GetOptions{})
	if err != nil{
		panic(err)
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(getDeployEvent(newDepl))
	if err != nil{
		panic(err)
	}
	return
}

func getDeploymentPods(deploymentName string,namespace string) (*[]corev1.Pod,error) {
	fmt.Printf("getPodsOf called with deployment %s\n",deploymentName)
	kubeclient := GetKubeClient()
	deployment,err := kubeclient.AppsV1().Deployments(namespace).Get(deploymentName,metav1.GetOptions{})
	if err != nil {
		panic(err)
	}
	labelSelector := labels.NewSelector()
	for labelKey, labelVal := range deployment.Spec.Selector.MatchLabels {
		requirement, err := labels.NewRequirement(labelKey, selection.Equals, []string{labelVal})
		if err != nil {
			fmt.Println("Unable to create selector requirement\n")
			return nil,err
		}
		labelSelector = labelSelector.Add(*requirement)
	}
	fmt.Printf("labelSelector: %s\n",labelSelector.String())
	runningOnlyRule := "status.phase=Running"
	options := metav1.ListOptions{
		LabelSelector:       labelSelector.String(),
		FieldSelector:       runningOnlyRule,
	}
	pods, err := kubeclient.CoreV1().Pods(deployment.Namespace).List(options)
	return &pods.Items,err
}

func GetDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Inside GetDeploymentLogs func\n")
	vars := mux.Vars(r)
	ns := vars["ns"]
	dep := vars["dep"]
	fmt.Printf("dep %s in %s \n", dep, ns)
	kubeclient := GetKubeClient()
	pods,err := getDeploymentPods(dep,ns)
	if err != nil {
		panic(err)
	}
	if len(*pods) == 0 {
		fmt.Errorf("no pods found for deployment %s",dep)
	}
	tail := int64(50)
	podLogOpts := corev1.PodLogOptions{TailLines: &tail}
	req := kubeclient.CoreV1().Pods(ns).GetLogs((*pods)[0].Name, &podLogOpts)
	podLogs, err := req.Stream()
	if err != nil {
		panic(err)
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(buf.Bytes())
	if err != nil{
		panic(err)
	}
	return
}

func GetDeployments(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Inside GetDeployments func\n")
	fmt.Printf("ns %s \n", mux.Vars(r)["ns"])
	kubeclient := GetKubeClient()
	deploymetns, err := kubeclient.AppsV1().Deployments(mux.Vars(r)["ns"]).List(metav1.ListOptions{})
	if err != nil{
		panic(err)
	}
	depList := deployments{
		[]string{},
	}
	for _, dep := range deploymetns.Items{
		depList.ID = append(depList.ID, dep.Name)
	}
	b, err := json.Marshal(depList)
	if err != nil {
		fmt.Println(err)
		return
	}
	_, _ = w.Write(b)
	return
}

func StreamDeployments(h *Hub) {
	fmt.Printf("Inside StreamDeployments func\n")
	kubeclient := GetKubeClient()

	factory := informers.NewSharedInformerFactory(kubeclient, 0)
	deploymentInformer := factory.Apps().V1().Deployments().Informer()
	stopper := make(chan struct{})
	defer close(stopper)

	deploymentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
		UpdateFunc: func(old, new interface{}) {
			newDepl := new.(*appsv1.Deployment)
			fmt.Printf("deploymet %s changed\n", newDepl.Name)

			h.broadcast <- getDeployEvent(newDepl)

			return
		},
		DeleteFunc: func(obj interface{}) {},
	})

	deploymentInformer.Run(stopper)
}

func getDeployEvent(newDepl *appsv1.Deployment) []byte{
	deploy := DeploymentEvent{
		Name: newDepl.Name,
		Namespace: newDepl.Namespace,
		ReplicaCurrent: fmt.Sprintf("%d", newDepl.Status.AvailableReplicas),
		ReplicaDesired: fmt.Sprintf("%d", newDepl.Status.Replicas),
		Containers: []Container{},
		Age: calcAge(newDepl.Status.Conditions[0].LastUpdateTime),
	}

	for _ ,c := range newDepl.Spec.Template.Spec.Containers{
		var con = Container{}
		con.Name = c.Name
		con.Image = c.Image
		con.Envs = []string{}
		for _, env := range c.Env{
			if env.Value == ""{
				con.Envs = append(con.Envs, env.Name+":********")
			}else{
				con.Envs = append(con.Envs, env.Name+":"+env.Value)
			}
		}
		deploy.Containers = append(deploy.Containers, con)
	}

	b, err := json.Marshal(deploy)
	if err != nil {
		fmt.Println(err)
		return nil
	}
	return b
}

func calcAge(depCreationTime metav1.Time) string{
	format := "2006-01-02T15:04:05.000Z"
	then,_ := time.Parse(format, depCreationTime.Format(format))

	date , _:= time.Parse(format, time.Now().Format(format))

	diff := date.Sub(then)

	d := int(diff.Hours())
	h := int(diff.Hours())
	m := int(diff.Minutes())
	fmt.Println("before:")
	fmt.Println(strconv.Itoa(d) + "d" + strconv.Itoa(h) + "h" + strconv.Itoa(m) + "m")
	if d > 0{
		fmt.Println("1")
		d = d/24
		fmt.Println("2")
		if d > 0 {
			h = h % d
		}
	}else{
		fmt.Println("3")
		d = 0
	}
	if h > 0{
		fmt.Println("4")
		m = m%h
	}
	fmt.Println("after:")
	fmt.Println(strconv.Itoa(d) + "d" + strconv.Itoa(h) + "h" + strconv.Itoa(m) + "m")
	return strconv.Itoa(d) + "d" + strconv.Itoa(h) + "h" + strconv.Itoa(m) + "m"
}
