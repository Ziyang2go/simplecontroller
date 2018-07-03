package main

import (
	"fmt"
	"log"
	"sync"
	"time"
	"strings"
	// apierrors "k8s.io/apimachinery/pkg/api/errors"
	//"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	// "k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	informerbatchv1 "k8s.io/client-go/informers/batch/v1"
	"k8s.io/client-go/kubernetes"
	// "k8s.io/client-go/kubernetes/scheme"
	batchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	listerbatchv1 "k8s.io/client-go/listers/batch/v1"
	// apicorev1 "k8s.io/client-go/pkg/api/v1"
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
	mongoSvc   MongoSVC
}


func NewJobController(client *kubernetes.Clientset,
	jobInformer informerbatchv1.JobInformer, mongoSvc MongoSVC) *JobController {
	c := &JobController{
		jobGetter: 						 client.BatchV1(),
		jobLister: 						 jobInformer.Lister(),
		jobListerSynced:			jobInformer.Informer().HasSynced,
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
				c.EnqueJobs(key)
			},

			UpdateFunc: func(oldObj, newObj interface{}) {
				log.Print("Jobs updated")
				// key, err := cache.MetaNamespaceKeyFunc(newObj)
				// if err != nil {
				// 	log.Printf("onUpdate key error for %#v: %v", newObj, err)
				// 	runtime.HandleError(err)
				// }
				//c.UpdateJob(key)
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
	// pull the next work item from queue.  It should be a key we use to lookup
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
	// if ns != targetNs {
	// 	log.Print("ignore different namespace jobs")
	//  	return nil
	// }
	kubeJob, err := c.jobLister.Jobs(ns).Get(jobName)
	if err != nil {
		return err
	}
	jobSucceed := kubeJob.Status.Succeeded
	jobFailed := kubeJob.Status.Failed
	log.Print("update succeed job ", jobSucceed)
	log.Print("upddate failed job ", jobFailed)
	mongoErr := c.mongoSvc.Create(jobName, "Pending")
	if mongoErr != nil {
		log.Printf("Save to mongo error $v", mongoErr)
	}
	return nil
}

func (c *JobController) UpdateJob(key interface{}) error {
	arr := strings.Split(key.(string), "/")
	ns, jobName := arr[0], arr[1]
	// if ns != targetNs {
	// 	log.Print("ignore different namespace jobs")
	//  	return nil
	// }
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
	if status == "ok" {
		jobLog, _ = c.getJobLogs(key)
	}
	_, mongoErr := c.mongoSvc.Update(jobName, status, jobLog)
	if mongoErr != nil {
		log.Printf("Save to mongo error %v", mongoErr)
	}
	return nil
}

func (c *JobController) getJobLogs(key interface{}) (string, error) {
	// arr := strings.Split(key.(string), "/")
	// ns, jobName := arr[0], arr[1]
	// kubeJob, err := c.jobLister.Jobs(ns).Get(jobName)
	// if err != nil {
	// 	return "", err
	// }
	return "123", nil
}

// func (c *JobController) getSecretsInNS(ns string) ([]*apicorev1.Secret, error) {
// 	rawSecrets, err := c.secretLister.Secrets(ns).List(labels.Everything())
// 	if err != nil {
// 		return nil, err
// 	}

// 	var secrets []*apicorev1.Secret
// 	for _, secret := range rawSecrets {
// 		if _, ok := secret.Annotations[secretSyncAnnotation]; ok {
// 			secrets = append(secrets, secret)
// 		}
// 	}
// 	return secrets, nil
// }

func (c *JobController) StartJob(key interface{}) error {
	log.Printf("Starting work", key)
	// srcSecrets, err := c.getSecretsInNS(secretSyncSourceNamespace)
	// if err != nil {
	// 	return err
	// }

	// rawNamespaces, err := c.namespaceLister.List(labels.Everything())
	// if err != nil {
	// 	return err
	// }
	// var targetNamespaces []*apicorev1.Namespace
	// for _, ns := range rawNamespaces {
	// 	if _, ok := ns.Annotations[secretSyncAnnotation]; ok {
	// 		targetNamespaces = append(targetNamespaces, ns)
	// 	}
	// }

	// for _, ns := range targetNamespaces {
	// 	c.SyncNamespace(srcSecrets, ns.Name)
	// }

	log.Print("Finishing", key)
	return nil
}

// func (c *JobController) SyncNamespace(secrets []*apicorev1.Secret, ns string) {
// 	// 1. Create/Update all of the secrets in this namespace
// 	for _, secret := range secrets {
// 		newSecretInf, _ := scheme.Scheme.DeepCopy(secret)
// 		newSecret := newSecretInf.(*apicorev1.Secret)
// 		newSecret.Namespace = ns
// 		newSecret.ResourceVersion = ""
// 		newSecret.UID = ""

// 		log.Printf("Creating %v/%v", ns, secret.Name)
// 		_, err := c.secretGetter.Secrets(ns).Create(newSecret)
// 		if apierrors.IsAlreadyExists(err) {
// 			log.Printf("Scratch that, updating %v/%v", ns, secret.Name)
// 			_, err = c.secretGetter.Secrets(ns).Update(newSecret)
// 		}
// 		if err != nil {
// 			log.Printf("Error adding secret %v/%v: %v", ns, secret.Name, err)
// 		}
// 	}

// 	// 2. Delete secrets that have annotation but are not in our src list
// 	srcSecrets := sets.String{}
// 	targetSecrets := sets.String{}

// 	for _, secret := range secrets {
// 		srcSecrets.Insert(secret.Name)
// 	}

// 	targetSecretList, err := c.getSecretsInNS(ns)
// 	if err != nil {
// 		log.Printf("Error listing secrets in %v: %v", ns, err)
// 	}
// 	for _, secret := range targetSecretList {
// 		targetSecrets.Insert(secret.Name)
// 	}

// 	deleteSet := targetSecrets.Difference(srcSecrets)
// 	for secretName, _ := range deleteSet {
// 		log.Printf("Delete %v/%v", ns, secretName)
// 		err = c.secretGetter.Secrets(ns).Delete(secretName, nil)
// 		if err != nil {
// 			log.Printf("Error deleting %v/%v: %v", ns, secretName, err)
// 		}
// 	}
// }
