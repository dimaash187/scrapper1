# GoLANG Scrapper1 BACKEND

This project is a backend server to run Web Crawling jobs to extract URLs of imagesl. Makefile provided. The project is run on the port 8081.
It exposes 2 endpoints:

    1. /jobs [ POST request ]
    2. /job/{jobid} [ DELETE request with a string jobid argument ], this cancels currently running job with its associated workers.
    3. /job/{jobid}/status [ GET request with a string jobid argument ], get a status of a running job.
    4. /job/{jobid}/result [ GET request with a string jobid argument ], get a result of a running job.
    5. /all [ GET request ], get an output of a global data structure that holds all jobs. This will show the details of each job and how the data it getting asynchronously accumulated in the background.

## Build

Run `make build` to build the project. Main executable will be generated in the project root. 
Also a docker image [ app1 ] can be built with `make build-docker`.

Then run the docker image: `docker run -p 8081:8081 scrapper1`

## Running scenarios

1. POSTING a new Job:

`curl -X POST 'http://localhost:8001/jobs' -H "Content-Type: application/json" --data '{"urls":["http://4chan.org/","https://golang.org/"],"workers":2}'`


2. Getting a job's status:

`curl 'http://localhost:8001/jobs/047500fa-37e3-4a80-a9fe-fdda1e6f1150/status'`


3. Getting a job's result:

`curl 'http://localhost:8001/jobs/047500fa-37e3-4a80-a9fe-fdda1e6f1150/result'`


4. Canceling a job:

`curl -X DELETE 'http://localhost:8001/jobs/047500fa-37e3-4a80-a9fe-fdda1e6f1150'`


5. Getting all jobs:

`curl 'http://localhost:8001/all'`


## Running unit tests

Run `make test` to execute the unit tests, WHICH WE DONT'T HAVE yet...

## Thoughts

The design pattern used for this task is a Producer/Consumer. 
A Web framework is used as a "produced" which receives an HTTP POST request, parses it and extracts necessary parameters to create a "Job".
A "Job" is placed in the storage. A certain amount of workers are spawned that will process a set of Tasks [ created based on either # of Workers in the POST Payload OR the # of URLs to crawl ].

Each consumer worker will take a Task from a "jobs" channel and proces it. The "Crawling" process is done within a workers body, where each URL is visited, then DOM model crawled and images are collected in the recursive manner.

There is an option to provide a "timeout" argument when starting the server: `./main --timeout=60` where a timeout is in seconds. This will make sure that Tasks will be terminated after the given timeout threshold.

Also the app keeps track of "avaliable" workers that it has. Each worker that consumer spawns will decrease a counter. After each job is done, consumer will increment that same counter.

This way if this service is called [ HTTP request ] when no more workers are avaliable, the request will be rejected [ sort of a throtlleing mechanism ].

There is an extra functionality to cancel a running job [ see endpoint # 2 above ].

And an extra endpoint to display all of the running jobs and their details.


One of the things that can certainly be improved is the unit testsing [ or integration/functional testing ].
The code needs to be designed with a smaller moving parts that can be tested individually. Also the overall functionality of the system needs to be tested.

One difficulty with the automated testing is the fact that requests are made outside [ external HTTP requests to the Links to crawl URLs ]. 
This behaviour is a somewhat difficulat to actually have properly tested without resorting to too much mocking.




