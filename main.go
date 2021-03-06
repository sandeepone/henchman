package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"path"
	"strings"
	"sync"

	"code.google.com/p/go.crypto/ssh"
	"code.google.com/p/gopass"

	"github.com/sudharsh/henchman/lib"
)

func currentUsername() *user.User {
	u, err := user.Current()
	if err != nil {
		panic("Couldn't get current username: " + err.Error())
	}
	return u
}

func defaultKeyFile() string {
	u := currentUsername()
	return path.Join(u.HomeDir, ".ssh", "id_rsa")
}

// Split args from the cli that are of the form,
// "a=x b=y c=z" as a map of form { "a": "b", "b": "y", "c": "z" }
// These plan arguments override the variables that may be defined
// as part of the plan file.
func parseExtraArgs(args string) henchman.TaskVars {
	extraArgs := make(henchman.TaskVars)
	if args == "" {
		return extraArgs
	}
	for _, a := range strings.Split(args, " ") {
		kv := strings.Split(a, "=")
		extraArgs[kv[0]] = kv[1]
	}
	return extraArgs
}

// TODO: Modules
func validateModulesPath() (string, error) {
	_modulesDir := os.Getenv("HENCHMAN_MODULES_PATH")
	if _modulesDir == "" {
		cwd, _ := os.Getwd()
		_modulesDir = path.Join(cwd, "modules")
	}
	modulesDir := flag.String("modules", _modulesDir, "Path to the modules")
	_, err := os.Stat(*modulesDir)
	return *modulesDir, err
}

func main() {
	username := flag.String("user", currentUsername().Username, "User to run as")
	usePassword := flag.Bool("password", false, "Use password authentication")
	keyfile := flag.String("private-keyfile", defaultKeyFile(), "Path to the keyfile")
	extraArgs := flag.String("args", "", "Extra arguments for the plan")

	modulesDir, err := validateModulesPath()
	if err != nil {
		log.Fatalf("Couldn't stat modules path '%s'\n", modulesDir)
	}
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [args] <plan>\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	planFile := flag.Arg(0)
	if planFile == "" {
		flag.Usage()
		os.Exit(1)
	}

	if *username == "" {
		fmt.Fprintf(os.Stderr, "Missing username")
		os.Exit(1)
	}

	// We support two SSH authentications methods for now
	// password and client key bases. Both are mutually exclusive and password takes
	// higher precedence
	var sshAuth ssh.AuthMethod
	if *usePassword {
		var password string
		if password, err = gopass.GetPass("Password:"); err != nil {
			log.Fatalf("Couldn't get password: " + err.Error())
			os.Exit(1)
		}
		sshAuth, err = henchman.PasswordAuth(password)
	} else {
		sshAuth, err = henchman.ClientKeyAuth(*keyfile)
	}
	if err != nil {
		log.Fatalf("SSH Auth prep failed: " + err.Error())
	}
	config := &ssh.ClientConfig{
		User: *username,
		Auth: []ssh.AuthMethod{sshAuth},
	}

	planBuf, err := ioutil.ReadFile(planFile)
	if err != nil {
		log.Fatalf("Error reading plan - %s\n", planFile)
		os.Exit(1)
	}

	var plan *henchman.Plan
	parsedArgs := parseExtraArgs(*extraArgs)
	plan, err = henchman.NewPlanFromYAML(planBuf, &parsedArgs)
	if err != nil {
		log.Fatalf("Couldn't read the plan: %s", err)
		os.Exit(1)
	}

	// Execute the same plan concurrently across all the machines.
	// Note the tasks themselves in plan are executed sequentially.
	wg := new(sync.WaitGroup)
	machines := henchman.Machines(plan.Hosts, config)
	localhost := henchman.Machine{"127.0.0.1", 0, nil}
	for _, _machine := range machines {
		machine := _machine
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, task := range plan.Tasks {
				var status *henchman.TaskStatus
				var err error
				if task.LocalAction {
					log.Printf("Local action detected\n")
					status, err = task.Run(&localhost, plan.Vars)
				} else {
					status, err = task.Run(machine, plan.Vars)
				}
				plan.SaveStatus(&task, status.Status)
				if err != nil {
					log.Printf("Error when executing task: %s\n", err.Error())
				}
				if status.Status == "failure" {
					log.Printf("Task was unsuccessful: %s\n", task.Id)
					break
				}
			}
		}()
	}
	wg.Wait()
	plan.PrintReport()
}
