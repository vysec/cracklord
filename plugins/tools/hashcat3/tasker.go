package hashcat3

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/jmmcatee/cracklord/common"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Tasker is the structure that implements the Tasker inteface
type Tasker struct {
	mux           sync.Mutex // Used for locking componets of the Tasker
	job           common.Job
	wd            string
	exec          exec.Cmd
	start         []string
	resume        []string
	showPot       []string
	showPotLeft   []string
	showPotOutput [][]string
	stderr        *bytes.Buffer
	stderrCp      bool
	stdout        *bytes.Buffer
	stdoutCp      bool
	stderrPipe    io.ReadCloser
	stdoutPipe    io.ReadCloser
	stdinPipe     io.WriteCloser

	doneWG sync.WaitGroup // Used for checking if the job is done
}

// Status returns the common.Job option of the Tasker
func (t *Tasker) Status() common.Job {
	log.WithField("task", t.job.UUID).Debug("Status call for hashcat3 Tasker")

	t.mux.Lock()
	defer t.mux.Unlock()

	if t.job.Status == common.STATUS_RUNNING {
		if !t.stderrCp {
			go func() {
				t.stderrCp = true
				for t.job.Status == common.STATUS_RUNNING {
					cpNE, err := io.Copy(t.stderr, t.stderrPipe)
					if err != nil {
						log.WithField("error", err.Error()).Warn("Error copying from CMD Stderr Pipe.")
					}

					log.WithFields(log.Fields{
						"stderrCount": cpNE,
					}).Debug("Number of bytes copied from Stderr of CMD.")
				}
				t.stderrCp = false
			}()
		}

		if !t.stdoutCp {
			go func() {
				t.stdoutCp = true
				for t.job.Status == common.STATUS_RUNNING {
					cpNO, err := io.Copy(t.stdout, t.stdoutPipe)
					if err != nil {
						log.WithField("error", err.Error()).Warn("Error copying from CMD Stdout Pipe.")
					}

					log.WithFields(log.Fields{
						"stdoutCount": cpNO,
					}).Debug("Number of bytes copied from Stdout of CMD.")
				}
				t.stdoutCp = false
			}()
		}

		if t.stdout.Len() != 0 {
			status := ParseMachineOutput(t.stdout.String())

			if t.job.PerformanceTitle == "" {
				t.job.PerformanceTitle = "MH/s"
			}
			t.job.Progress = status.Progress
			t.job.ETC = status.EstimateTime

			var totalSpeed float64
			for i := range status.Speed {
				totalSpeed += status.Speed[i]
			}
			t.job.PerformanceData[fmt.Sprintf("%d", time.Now().Unix())] = fmt.Sprintf("%f", totalSpeed/1000000)

			t.job.CrackedHashes = status.RecoveredHashes
			t.job.TotalHashes = status.TotalHashes
		}

		if t.stderr.Len() != 0 {
			t.job.Error = t.stderr.String()
		}
	}

	// Get the hash file
	var tempOutputData [][]string
	hashFile, err := os.Open(filepath.Join(t.wd, "output.txt"))
	if err == nil {
		defer hashFile.Close()

		// Pull the lines from the file for each individual hash
		hashScanner := bufio.NewScanner(hashFile)
		for hashScanner.Scan() {
			// Parse the line with the default separator |
			parts := strings.Split(hashScanner.Text(), config.Separator)

			// Add the parts to the output array
			if len(parts) == 2 {
				tempOutputData = append(tempOutputData, []string{parts[1], parts[0]})
			} else {
				log.Error("Split hash line on | and did not get 2 values.")
			}
		}
	}
	if err != nil {
		log.WithField("io_error", err).Error("Failed to open output.txt")
	}

	// Add in the pot file items
	for i := range t.showPotOutput {
		tempOutputData = append(tempOutputData, t.showPotOutput[i])
	}

	if len(tempOutputData) != 0 {
		t.job.OutputData = tempOutputData
	}

	t.stderr.Reset()
	t.stdout.Reset()

	return t.job
}

// Run starts or resumes the job
func (t *Tasker) Run() error {
	// Get the tasker luck so we can do some work on the job
	t.mux.Lock()
	defer t.mux.Unlock()

	// Check that we have not already finished this job
	if t.job.Status == common.STATUS_DONE || t.job.Status == common.STATUS_QUIT || t.job.Status == common.STATUS_FAILED {
		log.WithField("Status", t.job.Status).Debug("Unable to start hashcat3 job as it is done.")
		return errors.New("Job already finished.")
	}

	// Check if this job is running
	if t.job.Status == common.STATUS_RUNNING {
		// Job already running so return no errors
		log.Debug("hashcat3 job already running, doing nothing")
		return nil
	}

	// Execute the Hashcat --show command to get any potfile entries
	showExec := exec.Command(config.BinPath, t.showPot...)
	showExec.Dir = t.wd
	log.WithField("Show Command", showExec.Args).Debug("Executing Show Command")
	showPotStdout, err := showExec.Output()
	if err != nil {
		log.WithField("execError", err).Error("Error running hashcat --show command.")
	}
	log.WithField("showStdout", string(showPotStdout)).Debug("Show command stdout.")
	t.showPotOutput = ParseShowPotOutput(string(showPotStdout))

	showLeftExec := exec.Command(config.BinPath, t.showPotLeft...)
	showLeftExec.Dir = t.wd
	log.WithField("Left Command", showLeftExec.Args).Debug("Executing Left Command")
	showPotLeftStdout, err := showLeftExec.Output()
	if err != nil {
		log.WithField("execError", err).Error("Error running hashcat --left command.")
	}
	log.WithField("showLeftStdout", string(showPotLeftStdout)).Debug("Show Left command stdout.")
	leftOut := ParseShowPotLeftOutput(string(showPotLeftStdout))

	t.job.TotalHashes = int64(len(leftOut) + len(t.showPotOutput))
	t.job.CrackedHashes = int64(len(t.showPotOutput))

	// Set commands for restore or start
	if t.job.Status == common.STATUS_CREATED {
		t.exec = *exec.Command(config.BinPath, t.start...)
	} else {
		t.exec = *exec.Command(config.BinPath, t.resume...)
	}

	// Set the working directory
	t.exec.Dir = t.wd
	log.WithFields(log.Fields{
		"dir": t.exec.Dir,
	}).Debug("Setup working directory")

	// Assign the stderr, stdout, stdin pipes
	t.stderrPipe, err = t.exec.StderrPipe()
	if err != nil {
		return err
	}

	t.stdoutPipe, err = t.exec.StdoutPipe()
	if err != nil {
		return err
	}

	t.stdinPipe, err = t.exec.StdinPipe()
	if err != nil {
		return err
	}

	t.stderr = bytes.NewBuffer([]byte(""))
	t.stdout = bytes.NewBuffer([]byte(""))

	// Start the command
	log.WithField("argument", t.exec.Args).Debug("Running command.")
	err = t.exec.Start()
	t.doneWG.Add(1)
	if err != nil {
		// We had an error starting to return that and quit the job
		t.job.Status = common.STATUS_FAILED
		log.Errorf("There was an error starting the job: %v", err)
		return err
	}

	t.job.StartTime = time.Now()
	t.job.Status = common.STATUS_RUNNING

	go func() {
		// Wait for the job to finish
		t.exec.Wait()

		t.mux.Lock()
		t.job.Status = common.STATUS_DONE
		t.mux.Unlock()

		// Get the status now because we need the last output of hashes
		t.Status()

		t.doneWG.Done()
	}()

	return nil
}

// Pause kills the hashcat process and marks the job as paused
func (t *Tasker) Pause() error {
	log.WithField("task", t.job.UUID).Debug("Attempting to pause hashcat task")

	// Call status to update the job internals before pausing
	t.Status()

	if t.job.Status == common.STATUS_RUNNING {
		t.mux.Lock()

		if runtime.GOOS == "windows" {
			t.exec.Process.Kill()
		} else {
			io.WriteString(t.stdinPipe, "c")
		}

		t.mux.Unlock()

		// Wait for the program to actually exit
		t.doneWG.Wait()
	}

	// Change status to pause
	t.mux.Lock()
	t.job.Status = common.STATUS_PAUSED
	t.mux.Unlock()

	log.WithField("task", t.job.UUID).Debug("Task paused successfully")

	return nil
}

// Quit kills the hashcat process and then returns the most up-to-date status
func (t *Tasker) Quit() common.Job {
	log.WithField("task", t.job.UUID).Debug("Attempting to quit hashcat task")

	// Call status to update the job internals before quiting
	t.Status()

	if t.job.Status == common.STATUS_RUNNING || t.job.Status == common.STATUS_PAUSED {
		t.mux.Lock()

		if runtime.GOOS == "windows" {
			t.exec.Process.Kill()
		} else {
			io.WriteString(t.stdinPipe, "c")
		}

		t.mux.Unlock()

		// Wait for the program to actually exit
		t.doneWG.Wait()
	}

	t.mux.Lock()
	t.job.Status = common.STATUS_QUIT
	t.mux.Unlock()

	log.WithField("task", t.job.UUID).Debug("Task quit successfully")
	return t.job
}

// IOE is no longer used and is a empty interface for
func (t *Tasker) IOE() (io.Writer, io.Reader, io.Reader) {
	return nil, nil, nil
}
