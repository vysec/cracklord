package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/gorilla/mux"
	"github.com/jmmcatee/cracklord/common"
	"github.com/jmmcatee/cracklord/queue"
	"log"
	"net/http"
)

// All handler functions are created as part of the base AppController. This is done to
// allow type safe dependency injection to all handler functions. This also make
// expandablility related to adding a database or other dependencies much easier
// for future development.
type AppController struct {
	T    TokenStore
	Auth Authenticator
	Q    queue.Queue
}

func (a *AppController) Router() *mux.Router {
	r := mux.NewRouter().StrictSlash(false)

	// Login and Logout
	r.Path("/api/login").Methods("POST").HandlerFunc(a.Login)
	r.Path("/api/logout").Methods("GET").HandlerFunc(a.Logout)

	// Tools endpoints
	r.Path("/api/tools").Methods("GET").HandlerFunc(a.ListTools)
	r.Path("/api/tools/{id}").Methods("GET").HandlerFunc(a.GetTool)

	// Resource endpoints
	r.Path("/api/resources").Methods("GET").HandlerFunc(a.ListResource)
	r.Path("/api/resources").Methods("POST").HandlerFunc(a.CreateResource)
	r.Path("/api/resources/{id}").Methods("GET").HandlerFunc(a.ReadResource)
	r.Path("/api/resources/{id}").Methods("PUT").HandlerFunc(a.UpdateResources)
	r.Path("/api/resources/{id}").Methods("DELETE").HandlerFunc(a.DeleteResources)

	// Jobs endpoints
	r.Path("/api/jobs").Methods("GET").HandlerFunc(a.GetJobs)
	r.Path("/api/jobs").Methods("POST").HandlerFunc(a.CreateJob)
	r.Path("/api/jobs/{id}").Methods("GET").HandlerFunc(a.ReadJob)
	r.Path("/api/jobs/{id}").Methods("PUT").HandlerFunc(a.UpdateJob)
	r.Path("/api/jobs/{id}").Methods("DELETE").HandlerFunc(a.DeleteJob)

	return r
}

// Login Hander (POST - /api/login)
func (a *AppController) Login(rw http.ResponseWriter, r *http.Request) {
	// Decode the request and see if it is valid
	reqJSON := json.NewDecoder(r.Body)
	respJSON := json.NewEncoder(rw)

	var req = LoginReq{}
	var resp = LoginResp{}

	err := reqJSON.Decode(&req)
	if err != nil {
		// We had an error decoding the request to return an error
		resp.Status = RESP_CODE_BADREQ
		resp.Message = RESP_CODE_BADREQ_T
		resp.Token = ""

		// TODO: Eventually need to log this error
		rw.WriteHeader(RESP_CODE_BADREQ)
		respJSON.Encode(resp)
		return
	}

	// Verify the login
	user, err := a.Auth.Login(req.Username, req.Password)
	if err != nil {
		// Login failed so return error
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T
		resp.Token = ""

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
	}

	// Generate token
	seed := make([]byte, 256)
	bToken := sha256.New()

	rand.Read(seed)

	token := hex.EncodeToString(bToken.Sum(seed))

	// Add to the token store
	a.T.AddToken(token, user)

	// Return new information
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T
	resp.Token = token
	resp.Role = user.EffectiveRole()

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// Logout endpoint (POST - /api/logout)
func (a *AppController) Logout(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var resp = LogoutResp{}

	// Build the JSON Decoder
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	a.T.RemoveToken(token)

	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// List Tools endpoint (GET - /api/tools)
func (a *AppController) ListTools(rw http.ResponseWriter, r *http.Request) {
	// Resposne and Request structures
	var resp ToolsResp

	// JSON Encoder and Decoder
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for standard user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(StandardUser) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Get the tools list from the Queue
	for _, t := range a.Q.Tools() {
		resp.Tools = append(resp.Tools, APITool{t.UUID, t.Name, t.Version})
	}

	// Build response
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// Get Tool Endpoint (GET - /api/tools/{id})
func (a *AppController) GetTool(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var resp ToolsGetResp

	// JSON Encoder and Decoder
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for standard user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(StandardUser) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Get the tool ID
	uuid := mux.Vars(r)["id"]
	tool, ok := a.Q.Tools()[uuid]
	if !ok {
		// No tool found, return error
		resp.Status = RESP_CODE_NOTFOUND
		resp.Message = RESP_CODE_NOTFOUND_T

		rw.WriteHeader(RESP_CODE_NOTFOUND)
		respJSON.Encode(resp)
	}

	// We need to split the response from the tool into Form and Schema
	var form common.ToolJSONForm

	jsonBuf := bytes.NewBuffer([]byte(tool.Parameters))
	err := json.NewDecoder(jsonBuf).Decode(&form)
	if err != nil {
		log.Println(err)
		resp.Status = RESP_CODE_ERROR
		resp.Message = RESP_CODE_ERROR_T

		rw.WriteHeader(RESP_CODE_ERROR)
		respJSON.Encode(resp)
		return
	}

	// We found the tools so return it in the resp structure
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T
	resp.Tool.ID = tool.UUID
	resp.Tool.Name = tool.Name
	resp.Tool.Version = tool.Version
	resp.Tool.Form = &form.Form
	resp.Tool.Schema = &form.Schema

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// Get Job list (GET - /api/jobs)
func (a *AppController) GetJobs(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var resp GetJobsResp

	// JSON Encoder and Decoder
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Get the list of jobs and populate a return structure
	for _, j := range a.Q.AllJobs() {
		var job APIJob

		job.ID = j.UUID
		job.Name = j.Name
		job.Status = j.Status
		job.ResourceID = j.ResAssigned
		job.Owner = j.Owner
		job.StartTime = j.StartTime
		job.CrackedHashes = j.CrackedHashes
		job.TotalHashes = j.TotalHashes
		job.Progress = j.Progress
		job.ToolID = j.ToolUUID

		resp.Jobs = append(resp.Jobs, job)
	}

	// Return the results
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// Create a new job (POST - /api/job)
func (a *AppController) CreateJob(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var req JobCreateReq
	var resp JobCreateResp

	// JSON Encoder and Decoder
	reqJSON := json.NewDecoder(r.Body)
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for standard user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(StandardUser) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Decode the request
	err := reqJSON.Decode(&req)
	if err != nil {
		resp.Status = RESP_CODE_BADREQ
		resp.Message = RESP_CODE_BADREQ_T

		rw.WriteHeader(RESP_CODE_BADREQ)
		respJSON.Encode(resp)
		return
	}

	// Build a job structure
	job := common.NewJob(req.ToolID, req.Name, user.Username, req.Params)

	err = a.Q.AddJob(job)
	if err != nil {
		log.Println(err.Error())
		resp.Status = RESP_CODE_BADREQ
		resp.Message = RESP_CODE_BADREQ_T

		rw.WriteHeader(RESP_CODE_BADREQ)
		respJSON.Encode(resp)
		return
	}

	// Job was created so populate the response structure and return
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T
	resp.JobID = job.UUID

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// Read an individual Job (GET - /api/jobs/{id})
func (a *AppController) ReadJob(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var resp JobReadResp

	// JSON Encoder and Decoder
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Get the ID of the job we want
	jobid := mux.Vars(r)["id"]

	// Pull Job info from the Queue
	job := a.Q.JobInfo(jobid)

	// Build the response structure
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T
	resp.Job.ID = job.UUID
	resp.Job.Name = job.Name
	resp.Job.Status = job.Status
	resp.Job.ResourceID = job.ResAssigned
	resp.Job.Owner = job.Owner
	resp.Job.StartTime = job.StartTime
	resp.Job.CrackedHashes = job.CrackedHashes
	resp.Job.TotalHashes = job.TotalHashes
	resp.Job.Progress = job.Progress
	resp.Job.Params = job.Parameters
	resp.Job.ToolID = job.ToolUUID
	resp.Job.PerformanceTitle = job.PerformanceTitle
	resp.Job.PerformanceData = job.PerformanceData
	resp.Job.OutputTitles = job.OutputTitles
	resp.Job.OutputData = job.OutputData

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// Update a job
func (a *AppController) UpdateJob(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var req JobUpdateReq
	var resp JobUpdateResp

	// JSON Encoder and Decoder
	reqJSON := json.NewDecoder(r.Body)
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for standard user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(StandardUser) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Decode the request
	err := reqJSON.Decode(&req)
	if err != nil {
		resp.Status = RESP_CODE_BADREQ
		resp.Message = RESP_CODE_BADREQ_T

		rw.WriteHeader(RESP_CODE_BADREQ)
		respJSON.Encode(resp)
		return
	}

	// Get the ID of the job we want
	jobid := mux.Vars(r)["id"]

	// Get the action requested
	switch req.Status {
	case "pause":
		// Pause the job
		err = a.Q.PauseJob(jobid)
		if err != nil {
			resp.Status = RESP_CODE_ERROR
			resp.Message = RESP_CODE_ERROR_T

			rw.WriteHeader(RESP_CODE_ERROR)
			respJSON.Encode(resp)
			return
		}
	case "quit":
		// Stop the job
		err = a.Q.QuitJob(jobid)
		if err != nil {
			resp.Status = RESP_CODE_ERROR
			resp.Message = RESP_CODE_ERROR_T

			rw.WriteHeader(RESP_CODE_ERROR)
			respJSON.Encode(resp)
			return
		}
	}

	// Now return everything is good and the job info
	j := a.Q.JobInfo(jobid)

	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T
	resp.Job.ID = j.UUID
	resp.Job.Name = j.Name
	resp.Job.Status = j.Status
	resp.Job.ResourceID = j.ResAssigned
	resp.Job.Owner = j.Owner
	resp.Job.StartTime = j.StartTime
	resp.Job.CrackedHashes = j.CrackedHashes
	resp.Job.TotalHashes = j.TotalHashes
	resp.Job.Progress = j.Progress
	resp.Job.ToolID = j.ToolUUID

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

func (a *AppController) DeleteJob(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var resp JobDeleteResp

	// JSON Encoders and Decoders
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for standard user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(StandardUser) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Get the ID of the job we want
	jobid := mux.Vars(r)["id"]

	// Remove the job
	err := a.Q.RemoveJob(jobid)
	if err != nil {
		resp.Status = RESP_CODE_ERROR
		resp.Message = RESP_CODE_ERROR_T

		rw.WriteHeader(RESP_CODE_ERROR)
		respJSON.Encode(resp)
		return
	}

	// Job should now be removed, so return all OK
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

// List Resource API function
func (a *AppController) ListResource(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structure
	var resp ResListResp

	// JSON Encoders and Decoders
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for standard user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(StandardUser) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// List resources
	for _, r := range a.Q.GetResources() {
		var apires APIResource
		apires.ID = r.UUID
		apires.Name = r.Name
		if r.Paused {
			apires.Status = "paused"
		} else {
			apires.Status = "running"
		}
		apires.Address = r.Address

		for _, t := range r.Tools {
			apires.Tools = append(apires.Tools, APITool{t.UUID, t.Name, t.Version})
		}

		resp.Resources = append(resp.Resources, apires)
	}

	// Job should now be removed, so return all OK
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

func (a *AppController) CreateResource(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var req ResCreateReq
	var resp ResCreateResp

	// JSON Encoders and Decoders
	reqJSON := json.NewDecoder(r.Body)
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for Administrators user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(Administrator) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Decode the request
	err := reqJSON.Decode(&req)
	if err != nil {
		resp.Status = RESP_CODE_BADREQ
		resp.Message = RESP_CODE_BADREQ_T

		rw.WriteHeader(RESP_CODE_BADREQ)
		respJSON.Encode(resp)
		return
	}

	// Try and add the resource
	err = a.Q.AddResource(req.Address, req.Name, req.Key)
	if err != nil {
		resp.Status = RESP_CODE_ERROR
		resp.Message = RESP_CODE_ERROR_T

		rw.WriteHeader(RESP_CODE_ERROR)
		respJSON.Encode(resp)
		return
	}

	// Job should now be removed, so return all OK
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

func (a *AppController) ReadResource(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var req ResReadReq
	var resp ResReadResp

	// JSON Encoder and Decoder
	reqJSON := json.NewDecoder(r.Body)
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for standard user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(StandardUser) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Decode the request
	err := reqJSON.Decode(&req)
	if err != nil {
		resp.Status = RESP_CODE_BADREQ
		resp.Message = RESP_CODE_BADREQ_T

		rw.WriteHeader(RESP_CODE_BADREQ)
		respJSON.Encode(resp)
		return
	}

	// Get the resource ID
	resID := mux.Vars(r)["id"]

	// Get the resource
	for _, r := range a.Q.GetResources() {
		if resID == r.UUID {
			// Found the resource so set it to the response
			resp.Resource.ID = r.UUID
			resp.Resource.Name = r.Name
			resp.Resource.Address = r.Address
			if r.Paused {
				resp.Resource.Status = "paused"
			} else {
				resp.Resource.Status = "running"
			}

			for _, t := range r.Tools {
				resp.Resource.Tools = append(resp.Resource.Tools, APITool{t.UUID, t.Name, t.Version})
			}
		}
	}

	// TODO (mcatee): Add a check for no found resource and return correct status codes

	// Build good response
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

func (a *AppController) UpdateResources(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var req ResUpdateReq
	var resp ResUpdateResp

	// JSON Encoder and Decoder
	reqJSON := json.NewDecoder(r.Body)
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for Administrator user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(Administrator) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Decode the request
	err := reqJSON.Decode(&req)
	if err != nil {
		resp.Status = RESP_CODE_BADREQ
		resp.Message = RESP_CODE_BADREQ_T

		rw.WriteHeader(RESP_CODE_BADREQ)
		respJSON.Encode(resp)
		return
	}

	// Get the resource ID
	resID := mux.Vars(r)["id"]

	// Check the status change given
	if req.Status == "pause" {
		err = a.Q.PauseResource(resID)
		if err != nil {
			resp.Status = RESP_CODE_ERROR
			resp.Message = RESP_CODE_ERROR_T

			rw.WriteHeader(RESP_CODE_ERROR)
			respJSON.Encode(resp)
			return
		}
	}

	if req.Status == "resume" {
		err = a.Q.ResumeResource(resID)
		if err != nil {
			resp.Status = RESP_CODE_ERROR
			resp.Message = RESP_CODE_ERROR_T

			rw.WriteHeader(RESP_CODE_ERROR)
			respJSON.Encode(resp)
			return
		}
	}

	// TODO (mcatee): Add a check for no found resource and return correct status codes

	// Build good response
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}

func (a *AppController) DeleteResources(rw http.ResponseWriter, r *http.Request) {
	// Response and Request structures
	var resp ResDeleteResp

	// JSON Encoder and Decoder
	respJSON := json.NewEncoder(rw)

	// Get the authorization header
	token := r.Header.Get("AuthorizationToken")

	if !a.T.CheckToken(token) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Check for Administrator user level at least
	user, _ := a.T.GetUser(token)
	if !user.Allowed(Administrator) {
		resp.Status = RESP_CODE_UNAUTHORIZED
		resp.Message = RESP_CODE_UNAUTHORIZED_T

		rw.WriteHeader(RESP_CODE_UNAUTHORIZED)
		respJSON.Encode(resp)
		return
	}

	// Get the resource ID
	resID := mux.Vars(r)["id"]

	// Remove the resource
	err := a.Q.RemoveResource(resID)
	if err != nil {
		resp.Status = RESP_CODE_ERROR
		resp.Message = RESP_CODE_ERROR_T

		rw.WriteHeader(RESP_CODE_ERROR)
		respJSON.Encode(resp)
		return
	}

	// TODO (mcatee): Add a check for no found resource and return correct status codes

	// Build good response
	resp.Status = RESP_CODE_OK
	resp.Message = RESP_CODE_OK_T

	rw.WriteHeader(RESP_CODE_OK)
	respJSON.Encode(resp)
}
