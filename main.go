package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Ziyang2go/tgik-controller/config"
	"github.com/Ziyang2go/tgik-controller/mongo"
	"github.com/Ziyang2go/tgik-controller/version"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	log.Printf("controller version %s", version.VERSION)

	configPath := flag.String("config", "./config/config.json", "path of the config file")

	flag.Parse()
	mongoconf, loadErr := config.FromFile(*configPath)

	if loadErr != nil {
		log.Fatal(loadErr)
	}

	mongoSvc, mongoErr := mongo.New(mongoconf.Mongo.Host, mongoconf.Mongo.Port, mongoconf.Mongo.DB, mongoconf.Mongo.Collection)
	if mongoErr != nil {
		log.Fatal(mongoErr)
	}
	kubeconfig := ""
	flag.StringVar(&kubeconfig, "kubeconfig", kubeconfig, "kubeconfig file")
	flag.Parse()
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	var (
		config *rest.Config
		err    error
	)
	var gateway = mongoconf.Pushgateway
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating client: %v", err)
		os.Exit(1)
	}
	client := kubernetes.NewForConfigOrDie(config)
	sharedInformers := informers.NewSharedInformerFactory(client, 10*time.Minute)
	jobController := NewJobController(client, sharedInformers.Batch().V1().Jobs(), mongoSvc, gateway)
	sharedInformers.Start(nil)
	jobController.Run(nil)
}
