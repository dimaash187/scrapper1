package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

var workerPoolSize = runtime.NumCPU()
var avaliableWoerks = runtime.NumCPU()
var mu sync.Mutex
var defaultTimeout int32 = 300

// Payload IS A GENERAL DATA STRUCT THAT HOLDS THE JSON DATA THAT WAS POSTED [URLs, WORKERs]
// IT REPRESENTS A JOB REQUEST [1 PER EACH HTTP POST REQUEST] IDENTIFIED BY A UNIQUE UUID [JobID]
type Payload struct {
	Urls       []string `json:"urls"`
	Workers    int      `json:"workers"`
	Completed  int      `json:"completed"`
	InProgress int      `json:"in_progress"`
	JobID      string
	Output     string
	cancel     context.CancelFunc
	Results    map[string][]string
}

// TaskResult IS WHNEN ALL WORKERS FINISHED PROCESSING ALL OF THEIR JOBs TASKS
// JobID = Id OF A PARENT JOB TO WHICH THE REULSTS OF THIS PARTIAL TASK BELONG
// URL = URL WHICH WE ARE CRAWLING
// Output = STATUS OF THE JOB
// Links = MAP OF URLS AND HOW MUCH EACH WAS VISITED
type TaskResult struct {
	JobID  string
	Url    string
	Output string
	Links  map[string]int
}

// TaskIntermidiateResult IS A DATA STRUCT TO HOLD PARTIAL LIST OF COLLECTED IMAGES
// Id = Id OF A PARTICULAR TASK
// JobID = Id OF A PARENT JOB TO WHICH THE REULSTS OF THIS PARTIAL TASK BELONG
// URL = URL WHICH WE ARE CRAWLING
// Output = PARTIAL SLICE OF STRINGS THAT HAVE BEEN SO FAR ACCUMULATED
type TaskIntermidiateResult struct {
	Id     int
	JobID  string
	Url    string
	Output []string
}

// TASK IS A DATA STRUCT THAT REPRESENTS A UNIT OF WORK [A PARTICULAR URL]
// Id = Id OF A PARTICULAR TASK
// JobID = Id OF A PARENT JOB TO WHICH THE REULSTS OF THIS PARTIAL TASK BELONG
// URL = URL WHICH WE ARE CRAWLING
// Links = MAP OF URLS AND HOW MUCH EACH WAS VISITED
// intermidiateResults = CHANNEL ON WHICH PARTIAL RESULSTS ARE SENT
// close = CHANNEL TO SIGNAL A PARTICULAR TASK THAT IT IS TO BE ABORTED
type Task struct {
	Id                  int
	JobID               string
	Url                 string
	Links               map[string]int
	intermidiateResults chan TaskIntermidiateResult
	close               chan bool
}

type pageInfo struct {
	StatusCode int
	Links      map[string]int
}

// CONSUMER STRUCT USES A FanIn/FanOut PATTERN
// TO RECEIVE JOB's TASKS ON THE Intermitidate CHANNEL [ingestChan]
// THEN PIPE THEM TO A Jobs CHANNEL [jobsChan] FOR EACH WORKER TO PULL FROM
// THE RESULTS OF ALL WORKERS ARE THEN ACCUMULATED ON THE RESULTS [jobResultChannel] CHANNEL
// ingestChan = TEMPORARY "QUEUE" TO RECEIVE TASKS INTO
// jobsChan = A "QUEUE" OF JOB's TASKS FROM WHICH CONSUMER's WORKERS PULL TASK OFF
// jobResultChannel = A CHANNEL TO WHICH CONSUMER WORKERS SEND THE RESULTs
// canel = A CANCELATION TOKEN TO SIGNAL ALL CONSUMER'S WORKERS TO ABORT [ ex HTTP DELETE REQUEST TO CANCEL A JOB ]
// jobsBatch = A STACK OF JOBS THAT ARE GETTING ACCUMULATED BY HTTP ENDPOINT [ SIMULATING A DB HELD JOBS ]
// Done = A CHANNEL TO SIGNAL ALL CONSUMER' WORKERS TO ABORT
// mu = A MUTEXT TO PROTECT ACCESS TO CRITICAL SECTIONS OF CONSUMERS DATA ACCES [jobsBatch]
type Consumer struct {
	ingestChan       chan Task
	jobsChan         chan Task
	jobResultChannel chan TaskResult
	cancel           context.CancelFunc
	jobsBatch        []Payload
	Done             chan struct{}
	mu               sync.Mutex
}

var consumer = &Consumer{
	ingestChan:       make(chan Task, 1),
	jobsChan:         make(chan Task, workerPoolSize),
	jobResultChannel: make(chan TaskResult),
	Done:             make(chan struct{}),
}

// WORKER ROUTINEs KEEP PULLING JOBs TASKS FROM JOBS CHANNEL
// AND WILL BE LAUNCHING A "Crawler" PROCESS [ PARSING A URL AND COLLECTING IMAGES]
// ONCE THE WORKER FINISHES IT SENDS OUT A RESULT RESPONSE TO THE RECEIVING END SO IT CAN MARK THE JOB AS COMPLETED
func (c Consumer) workerFunc(wg *sync.WaitGroup, index int) {
	defer wg.Done()

	log.Printf("Worker %d starting\n", index)

	for {
		select {
		case task, ok := <-c.jobsChan:
			if ok {
				log.Printf("Worker %d started job task # %d beloging to jobId %s \n", index, task.Id, task.JobID)
				Crawl(task, task.intermidiateResults)
				log.Printf("Worker %d finished processing job %d beloging to jobId %s \n", index, task.Id, task.JobID)
				t := TaskResult{Output: "Success", JobID: task.JobID, Url: task.Url, Links: task.Links}
				c.jobResultChannel <- t
			} else {
				log.Println("===> workerFunc got closed jobsChan <===")
				return
			}
		}
	}
	log.Printf("Worker %d interrupted\n", index)
}

// CONSUMER ROUTINE KEEPS INGESTING JOBS FROM THE TEMPORARY CHAN
// IT WILL PIPE THE JOBs TASKS INTO THE JOBs CHAN
func (c Consumer) startConsumer(ctx context.Context) {
	for {
		select {
		case job := <-c.ingestChan:
			c.jobsChan <- job
		case <-ctx.Done():
			log.Println("Consumer received cancellation signal, closing jobsChan!")
			// close(c.jobsChan)
			// fmt.Println("Consumer closed jobsChan")
			return
		}
	}
}

// WHEN PARTICULAR TASKS SEND PARTIAL RESULTS [ LIST OF PARSED IMAGE URLS ]
// TAKE THE RESULTS FROM THE Intermidiate RESULTS CHANNEL
func (c Consumer) fanOutConsumerTask(ctx context.Context, task Task) {
	for {
		select {
		// A PARTICULAR TASK WILL BE RECEIVING TEMPORARY RESULTS [ BURSTS OF PARSED IMAGE URLS ] ON THIS CHANNEL
		case tempResult := <-task.intermidiateResults:
			// fmt.Printf("===> receiving from task.intermidiateResults for jobid = %s, taskid = %d, url = %s <===\n", tempResult.JobID, tempResult.Id, tempResult.Url)
			log.Printf("url is %s \n", tempResult.Url)
			log.Print(tempResult.Output)

			log.Println("------------------------------------------------------------------------------")
			c.mu.Lock()
			// LETS FIND THE PARENT JOB OF THE TASK IN QUESTION AND APPEND THE PARTIAL LIST OF URLs TO ITS RESULTS
			for i := range consumer.jobsBatch {
				if consumer.jobsBatch[i].JobID == tempResult.JobID {
					consumer.jobsBatch[i].Results[tempResult.Url] = append(consumer.jobsBatch[i].Results[tempResult.Url], tempResult.Output...)
				}
			}
			c.mu.Unlock()

		case <-ctx.Done():
			log.Println("===> fanOutConsumerTask received cancellation signal via ctx <===")
			task.close <- true
			return

		case <-c.Done:
			log.Println("===> fanOutConsumerTask received cancellation signal via channel <===")
			task.close <- true
			c.Done <- struct{}{}
			return
		}
	}
}

// WHEN ALL CONSUMER's ROUTINES FINISH THEIR WORK LETS GET RESULTS
// FROM THE JOB RESULTS CHANNEL, LOCATE THE PARENT JOB AND FLAG IT AS COMPLETED [STATUS, COMPLETED/INPROGRESS COUNTERS]
func (c Consumer) fanOutConsumer(ctx context.Context) {

	for {
		select {
		case result := <-c.jobResultChannel:
			log.Printf("receiving from jobResultChannel for JobID %s processing URL %s...\n", result.JobID, result.Url)

			c.mu.Lock()
			for i := range consumer.jobsBatch {
				if consumer.jobsBatch[i].JobID == result.JobID {
					log.Println("===> FOUND ORIGINAL JOBID <===")
					consumer.jobsBatch[i].Output = result.Output
					consumer.jobsBatch[i].Completed++
					consumer.jobsBatch[i].InProgress--
				}
			}
			c.mu.Unlock()
			mu.Lock()
			avaliableWoerks = avaliableWoerks + 1
			mu.Unlock()
		}
	}
}

func GetImage(parentUrl string, s2 *[]string, lookup map[string]int) {

	doc, err := goquery.NewDocument(parentUrl)

	// SOMETIMES WE GET BAD IMAGE URLS
	// IF SO, LETS SILENECE THE EXCEPTION AND JUST EXIT
	// PROCEEDING TO NEXT LINK ON THE DOM
	if err != nil {
		// log.Fatal(err)
		return
	}

	doc.Find("img").Each(func(_ int, s *goquery.Selection) {

		url2, _ := s.Attr("src")

		// MAKE SURE TO ONLY COLLECT THE FOLLOWING IMAGE FORMATS
		if strings.HasSuffix(url2, "jpg") ||
			strings.HasSuffix(url2, "jpeg") ||
			strings.HasSuffix(url2, "png") ||
			strings.HasSuffix(url2, "gif") {

			if len(url2) > 0 {
				var s string
				if url2[0:2] == "//" {
					s = url2[2:]
				} else {
					parentUrl = strings.TrimSuffix(parentUrl, "/")
					s = fmt.Sprintf("%s%s", parentUrl, url2)
				}
				// IF WE HAVE NOT YET SEEN THIS IMAGE URL LETS COLLECT IT TO TEMP BUFF
				if _, ok := lookup[s]; !ok {
					lookup[s]++
					log.Println("image url: ", s)
					*s2 = append(*s2, s)
				}
			}
		}
	})

}

// A CRAWLER PROCESS THAT TRAVERSES RECUSIVELY THE DOCUMENT's LINKS
// AND COLLECTS THE IMAGES
func Crawl(task Task, intermidiateResultsChannel chan TaskIntermidiateResult) {

	var s2 []string
	lookup := make(map[string]int)

	u, err := url.Parse(task.Url)
	if err != nil {
		log.Fatal(err)
	}
	domain := strings.TrimSuffix(u.Hostname(), "/")
	glob := strings.TrimSuffix(u.Hostname(), "/")
	glob = strings.TrimSuffix(glob, filepath.Ext(glob))

	log.Println("PARSED DOMAIN: ", fmt.Sprintf("www.%s", u.Hostname()))
	log.Println("PARSED DomainGlob: ", glob)

	c := colly.NewCollector(
		colly.AllowedDomains(domain, fmt.Sprintf("www.%s", u.Hostname())),
		colly.MaxDepth(1),
		colly.Async(false),
	)
	// c.Async = false
	// c.AllowURLRevisit = false
	c.Limit(&colly.LimitRule{
		DomainGlob: fmt.Sprintf(".*%s.*", glob),
	})

	// TEMPORARY STORAGE OF ALL VISITED URLS
	p := &pageInfo{Links: make(map[string]int)}

	c.OnResponse(func(r *colly.Response) {
		p.StatusCode = r.StatusCode
	})
	c.OnError(func(r *colly.Response, err error) {
		p.StatusCode = r.StatusCode
	})
	c.OnRequest(func(r *colly.Request) {

		log.Println("Visiting: ", r.URL.String())

		GetImage(r.URL.String(), &s2, lookup)

		// BUFFER UP >= 10 IMAGE URLS and PIPE THEM THROUGH THE CHANNEL
		// TO THE LISTENTING END [BURSTING TEMPORARY URLs THAT WERE PARSED & THEN CLEARING BUFF]
		if len(s2) >= 10 {
			p := TaskIntermidiateResult{Id: task.Id, JobID: task.JobID, Url: task.Url}
			p.Output = s2
			task.intermidiateResults <- p
			s2 = nil
		}
	})

	// CALLBACK TO PARSE A HREF LINKS ON THE DOM DOCUMENT
	// IF THE LINK HAS NOT BEEN VISITED BEFORE WILL PROCEED INTO IT
	c.OnHTML("a[href]", func(e *colly.HTMLElement) {

		link := e.Request.AbsoluteURL(e.Attr("href"))

		// LETS MAKE SURE TO NOT TAKE THESE COMMON FILE FORMATS IF WE HAPPEN TO ENCOUNTER THEM
		if !strings.HasSuffix(link, "swf") &&
			!strings.HasSuffix(link, "pdf") &&
			!strings.HasSuffix(link, "doc") &&
			!strings.HasSuffix(link, "docx") &&
			!strings.HasSuffix(link, "xls") &&
			!strings.HasSuffix(link, "xlsx") &&
			!strings.HasSuffix(link, "ppt") &&
			!strings.HasSuffix(link, "pptx") &&
			!strings.HasSuffix(link, "mp3") &&
			!strings.HasSuffix(link, "mpa") &&
			!strings.HasSuffix(link, "aif") &&
			!strings.HasSuffix(link, "cda") &&
			!strings.HasSuffix(link, "mid") &&
			!strings.HasSuffix(link, "midi") &&
			!strings.HasSuffix(link, "wav") &&
			!strings.HasSuffix(link, "wma") &&
			!strings.HasSuffix(link, "wpl") &&
			!strings.HasSuffix(link, "7z") &&
			!strings.HasSuffix(link, "z") &&
			!strings.HasSuffix(link, "jpg") &&
			!strings.HasSuffix(link, "jpeg") &&
			!strings.HasSuffix(link, "png") &&
			!strings.HasSuffix(link, "gif") &&
			!strings.HasSuffix(link, "zip") &&
			!strings.HasSuffix(link, "tar") &&
			!strings.HasSuffix(link, "jpg") {

			if link != "" {

				select {
				// IN CASE TASK WAS SIGNALED FOR TERMINATION LETS ABORT
				case <-task.close:
					log.Println("===> TASK CLOSED...<===")
					task.close <- true
					return
				default:
				}

				// IF WE HAVE NOT YET SEEN THIS URL, PROCEED
				if _, ok := p.Links[link]; !ok {
					p.Links[link]++
					c.Visit(link)
				}
			}
		}
	})

	// LETS START CRAWLING PROCESS
	c.Visit(task.Url)

	// AT THIS POINT CRAWLING PROCESS IS FINISHED
	log.Println("===> $$$ DONE $$$ <===")

	// DUMP RESULTS
	b, err := json.Marshal(p)
	if err != nil {
		log.Println("failed to serialize response: ", err)
		return
	}
	log.Println("RESPONSE: ", string(b))

	s2 = nil
	lookup = nil
}

// A DELETE HTTP HANDLER TO "CANCEL" PROCESSING JOB
// RECEIVES a JOBID, FINDS A JOB WITHIN CONSUMER'S LIST OF JOBS AND SIGNALS ALL OF ITS TASKs FOR TERMINATION
func DeleteJob(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jobid := mux.Vars(r)["jobid"]

	log.Println("DeleteJob got: ", jobid)

	consumer.mu.Lock()
	for i := range consumer.jobsBatch {
		if consumer.jobsBatch[i].JobID == jobid {
			if consumer.jobsBatch[i].Output != "deleted" {
				log.Println("Found parent JobID %s", consumer.jobsBatch[i].JobID)
				consumer.jobsBatch[i].cancel()
				consumer.jobsBatch[i].Output = "deleted"
				consumer.mu.Unlock()
				http.Error(w, fmt.Sprintf("JobId %s deleted", jobid), http.StatusOK)
				return
			} else {
				consumer.mu.Unlock()
				http.Error(w, fmt.Sprintf("JobId %s already deleted", jobid), http.StatusBadRequest)
				return
			}
		}
	}
	consumer.mu.Unlock()
	http.Error(w, fmt.Sprintf("JobId %s not found", jobid), http.StatusBadRequest)
}

func Initialize(timeout int32) {
	log.Println("Initialize Consumer...")
	log.Println(timeout)
	ctx, cancel := context.WithCancel(context.Background())
	consumer.cancel = cancel
	go consumer.startConsumer(ctx)
	go consumer.fanOutConsumer(ctx)
	defaultTimeout = timeout
}

func GetJobStatus(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")
	jobid := mux.Vars(r)["jobid"]

	log.Println("GetJobStatus got: ", jobid)

	for i := range consumer.jobsBatch {
		if consumer.jobsBatch[i].JobID == jobid {

			b := struct {
				Completed   int `json:"completed"`
				In_progress int `json:"in_progress"`
			}{
				Completed:   consumer.jobsBatch[i].Completed,
				In_progress: consumer.jobsBatch[i].InProgress,
			}
			json.NewEncoder(w).Encode(b)
			return
		}
	}

	http.Error(w, fmt.Sprintf("JobId %s not found", jobid), http.StatusBadRequest)
}

func GetJobResult(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")
	jobid := mux.Vars(r)["jobid"]

	log.Println("GetJobResult got: ", jobid)

	for i := range consumer.jobsBatch {
		if consumer.jobsBatch[i].JobID == jobid {
			json.NewEncoder(w).Encode(consumer.jobsBatch[i].Results)
			return
		}
	}

	http.Error(w, fmt.Sprintf("JobId %s not found", jobid), http.StatusBadRequest)
}

func PostJobs(w http.ResponseWriter, r *http.Request) {

	var payloaddata Payload

	// UNMARSHAL JSON BODY INTO OUR DATA STRUCT
	if err := json.NewDecoder(r.Body).Decode(&payloaddata); err != nil {
		log.Print(err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// IF WE DONT HAVE ENOUGH AVALIABLE WORKERS TO TAKE THE JOB
	// LETS NOT BOTTLENECK OUR RESOURCES WITH PENDING JOBS AND THRROTTLE
	if payloaddata.Workers >= avaliableWoerks {
		http.Error(w, fmt.Sprintf("All avaliable workers are busy"), http.StatusBadRequest)
		return
	}

	// SETUP CONTEXT, CANCELATION TOKEN AND WAITGROUP
	// MAKE SURE THAT ALL OF THE JOBS' TASKS DO NOT TAKE MORE THEN 40s [SIMULATING CANCELATION OF BACKGROUND WORK THAT IS TAKING TOO LONG]

	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Duration(time.Second*time.Duration(defaultTimeout)))
	wg := &sync.WaitGroup{}

	// A JOB IS IDENTIFIED BY A UNIQUE UUID SO THAT WE CAN DISTINGUISH BETWEEN EACH JOB
	id := uuid.New()
	payloaddata.JobID = id.String()
	payloaddata.Output = "in_progress"
	payloaddata.cancel = cancelFunc
	payloaddata.Completed = 0
	payloaddata.Results = make(map[string][]string)

	// LETS CREATE WORKERS BASED ON # OF URLs OR # OF WORKERS
	if payloaddata.Workers > len(payloaddata.Urls) {
		wg.Add(len(payloaddata.Urls))
		for i := 0; i < len(payloaddata.Urls); i++ {
			go consumer.workerFunc(wg, i)
		}
		mu.Lock()
		avaliableWoerks = avaliableWoerks - len(payloaddata.Urls)
		mu.Unlock()
	} else {
		wg.Add(payloaddata.Workers)
		for i := 0; i < payloaddata.Workers; i++ {
			go consumer.workerFunc(wg, i)
		}
		mu.Lock()
		avaliableWoerks = avaliableWoerks - payloaddata.Workers
		mu.Unlock()
	}

	// LETS CREATE JOB's TASKS BASED ON QUANTITY OF URL TO PROCESS
	// BASICALLY A TASK PER URL
	for i := 0; i < len(payloaddata.Urls); i++ {
		log.Printf("Sending job task # %d\n", i)
		payloaddata.InProgress++
		task := Task{Id: i, JobID: payloaddata.JobID, Url: payloaddata.Urls[i], Links: make(map[string]int), close: make(chan bool, 1)}
		task.intermidiateResults = make(chan TaskIntermidiateResult)
		go consumer.fanOutConsumerTask(ctx, task)
		consumer.ingestChan <- task
	}

	// LETS MAKE SURE ITS SAFE TO ADD NEW PayloadData TO DATA STRUCT
	consumer.mu.Lock()
	consumer.jobsBatch = append(consumer.jobsBatch, payloaddata)
	consumer.mu.Unlock()

	log.Println("Current Transactions:")

	b := struct {
		JobID   string   `json:"job_id"`
		Workers int      `json:"workers"`
		Urls    []string `json:"urls"`
	}{
		JobID:   payloaddata.JobID,
		Workers: payloaddata.Workers,
		Urls:    payloaddata.Urls,
	}
	json.NewEncoder(w).Encode(b)
}

func GetAll(w http.ResponseWriter, r *http.Request) {

	// numCPUs := runtime.NumCPU()

	log.Println("avaliable workers: ", avaliableWoerks)
	json.NewEncoder(w).Encode(consumer.jobsBatch)
}
