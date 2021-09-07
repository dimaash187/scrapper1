package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	H "scrapper1/handlers"

	"github.com/gorilla/mux"
)

func main() {

	argsPtr := flag.Int("timeout", 300, "")
	flag.Parse()

	router := mux.NewRouter().StrictSlash(true)

	H.Initialize(int32(*argsPtr))

	router.HandleFunc("/jobs", H.PostJobs).Methods("POST")
	router.HandleFunc("/job/{jobid}", H.DeleteJob).Methods("DELETE")
	router.HandleFunc("/job/{jobid}/status", H.GetJobStatus).Methods("GET")
	router.HandleFunc("/job/{jobid}/result", H.GetJobResult).Methods("GET")
	router.HandleFunc("/all", H.GetAll).Methods("GET")

	fmt.Println("Serving transactions on port 8081")
	log.Fatal(http.ListenAndServe(":8081", router))
}
