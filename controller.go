package main

import (
	"fmt"
	"log"
	"sync"
	"time"
	"strings"
	"bytes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/pkg/api/v1"
	informerbatchv1 "k8s.io/client-go/informers/batch/v1"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	batchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	listerbatchv1 "k8s.io/client-go/listers/batch/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type MongoSVC interface {
	Create(string, string) (error)
	Update(string, string, string) (string, error)
	Close() error
}

const targetNs = "mythreekit"

type JobController struct {
	queue workqueue.RateLimitingInterface
	jobGetter  batchv1.JobsGetter
	jobLister  listerbatchv1.JobLister
	jobListerSynced cache.InformerSynced
	podGetter  corev1.PodsGetter
	mongoSvc   MongoSVC
}


func NewJobController(client *kubernetes.Clientset,
	jobInformer informerbatchv1.JobInformer, mongoSvc MongoSVC) *JobController {
	c := &JobController{
		jobGetter: 						 client.BatchV1(),
		jobLister: 						 jobInformer.Lister(),
		jobListerSynced:			jobInformer.Informer().HasSynced,
		podGetter:            client.CoreV1(),
		mongoSvc:             mongoSvc,
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "secretsync"),
	}

	jobInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				key, err := cache.MetaNamespaceKeyFunc(obj)
				if err != nil {
					log.Printf("onAdd key error for %#v: %v", obj, err)
					runtime.HandleError(err)
				}
				c.CreateJob(key)
			},

			UpdateFunc: func(oldObj, newObj interface{}) {
				log.Print("Jobs updated")
				key, err := cache.MetaNamespaceKeyFunc(newObj)
				if err != nil {
					log.Printf("onUpdate key error for %#v: %v", newObj, err)
					runtime.HandleError(err)
				}
				c.UpdateJob(key)
			},

			DeleteFunc: func(obj interface{}) {
				log.Print("Jobs deleted")
			},
		},
	)
	return c
}

func (c *JobController) Run(stop <-chan struct{}) {
	var wg sync.WaitGroup

	defer func() {
		// make sure the work queue is shut down which will trigger workers to end
		log.Print("shutting down queue")
		c.queue.ShutDown()

		// wait on the workers
		log.Print("shutting down workers")
		wg.Wait()

		log.Print("workers are all done")
	}()

	log.Print("waiting for cache sync")
	if !cache.WaitForCacheSync(
		stop,
		c.jobListerSynced) {
		log.Print("timed out waiting for cache sync")
		return
	}
	log.Print("caches are synced")

	go func() {
		// runWorker will loop until "something bad" happens. wait.Until will
		// then rekick the worker after one second.
		wait.Until(c.runWorker, time.Second, stop)
		// tell the WaitGroup this worker is done
		log.Print("worker done")
		wg.Done()
	}()

	// wait until we're told to stop
	log.Print("waiting for stop signal")
	<-stop
	log.Print("received stop signal")
}

func (c *JobController) runWorker() {
	// hot loop until we're told to stop.  processNextWorkItem will
	// automatically wait until there's work available, so we don't worry
	// about secondary waits
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem deals with one key off the queue.  It returns false
// when it's time to quit.
func (c *JobController) processNextWorkItem() bool {
	// pull the next work item from queue.  It should metav1e a key we use to lookup
	// something in a cache
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	// you always have to indicate to the queue that you've completed a piece of
	// work
	defer c.queue.Done(key)

	// do your work on the key.  This method will contains your "do stuff" logic
	err := c.UpdateJob(key)
	if err == nil {
		// if you had no error, tell the queue to stop tracking history for your
		// key. This will reset things like failure counts for per-item rate
		// limiting
		c.queue.Forget(key)
		return true
	}

	// there was a failure so be sure to report it.  This method allows for
	// pluggable error handling which can be used for things like
	// cluster-monitoring
	runtime.HandleError(fmt.Errorf("Updatejob failed with: %v", err))

	// since we failed, we should requeue the item to work on later.  This
	// method will add a backoff to avoid hotlooping on particular items
	// (they're probably still not going to work right away) and overall
	// controller protection (everything I've done is broken, this controller
	// needs to calm down or it can starve other useful work) cases.
	c.queue.AddRateLimited(key)

	return true
}

func (c *JobController) EnqueJobs(key string) {
	c.queue.Add(key)
}

func (c *JobController) CreateJob(key interface{}) error {
	arr := strings.Split(key.(string), "/")
	ns, jobName := arr[0], arr[1]
	if ns != targetNs {
		log.Print("ignore different namespace jobs")
	 	return nil
	}
	kubeJob, err := c.jobLister.Jobs(ns).Get(jobName)
	if err != nil {
		return err
	}
	jobSucceed := kubeJob.Status.Succeeded
	jobFailed := kubeJob.Status.Failed
	if jobSucceed == 1 || jobFailed == 1 {
		return nil
	}
	mongoErr := c.mongoSvc.Create(jobName, "Pending")
	if mongoErr != nil {
		log.Printf("Save to mongo error $v", mongoErr)
	}
	return nil
}

func (c *JobController) UpdateJob(key interface{}) error {
	arr := strings.Split(key.(string), "/")
	ns, jobName := arr[0], arr[1]
	if ns != targetNs {
		log.Print("ignore different namespace jobs")
	 	return nil
	}
	kubeJob, err := c.jobLister.Jobs(ns).Get(jobName)
	if err != nil {
		return err
	}
	jobSucceed := kubeJob.Status.Succeeded
	jobFailed := kubeJob.Status.Failed
	status := "working"
	if jobSucceed == 1 {
		status = "ok"
	}
	if jobFailed == 1 {
		status = "failed"
	}
	var jobLog string = ""
	if status == "ok" || status == "failed" {
		jobLog, _ = c.getJobLogs(ns, jobName)
		c.cleanUp(ns, jobName)
	}

	_, mongoErr := c.mongoSvc.Update(jobName, status, jobLog)
	if mongoErr != nil {
		log.Printf("Save to mongo error %v", mongoErr)
	}
	return nil
}

func (c *JobController) getJobLogs(ns string, jobName string) (string, error) {
	log.Print("Get logs for job ", jobName)
	jobPods, err := c.podGetter.Pods(ns).List(metav1.ListOptions{	LabelSelector: "job-name="+jobName })
	if err != nil {
		log.Printf("List pods error %v", err)
		return "", err
	}
	podName := jobPods.Items[0].Name;
	container := jobPods.Items[0].Spec.Containers[0].Name

	logOptions := &v1.PodLogOptions{
		Container: container,
		Follow:     false,
		Previous:   false,
		Timestamps: true,
	}
	logStream := c.podGetter.Pods(ns).GetLogs(podName, logOptions)
	readCloser, readErr := logStream.Stream()
	if readErr != nil {
			log.Printf("Read Stream error %v", readErr)
			return "", readErr
	}
	defer readCloser.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(readCloser)
	return buf.String(), nil
}

func (c *JobController) cleanUp(ns string, jobName string) {
	log.Print("Clean up job ", jobName)
	policy := metav1.DeletePropagationBackground
	err := c.jobGetter.Jobs(ns).Delete(jobName, &metav1.DeleteOptions{ PropagationPolicy: &policy })
	if err != nil {
		log.Printf("Clean up job error for %s %v", jobName, err)
	}
}
