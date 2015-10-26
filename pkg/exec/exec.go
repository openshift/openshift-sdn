package exec

import (
	"fmt"
	osexec "os/exec"
	"strings"
)

var testMode bool
var testPrograms map[string]string

type TestResult struct {
	command string
	output  string
	err     error
}

var testResults []TestResult

func SetTestMode() {
	testMode = true
	testPrograms = make(map[string]string)
}

func AddTestProgram(path string) {
	lastSlash := strings.LastIndex(path, "/")
	basename := path[lastSlash+1:]
	testPrograms[basename] = path
}

func AddTestResult(command string, output string, err error) {
	testResults = append(testResults, TestResult{command, output, err})
}

func LookPath(program string) (string, error) {
	if testMode {
		path, ok := testPrograms[program]
		if !ok {
			return "", fmt.Errorf("Not found: %s", program)
		}
		return path, nil
	}

	return osexec.LookPath(program)
}

func Exec(cmd string, args ...string) (string, error) {
	if testMode {
		var command string
		if len(args) > 0 {
			command = cmd + " " + strings.Join(args, " ")
		} else {
			command = cmd
		}

		if len(testResults) == 0 {
			panic(fmt.Sprintf("Ran out of testResults executing: %s", command))
		}

		result := testResults[0]
		testResults = testResults[1:]
		if command != result.command {
			panic(fmt.Sprintf("Wrong exec command: expected %v, got %v", result.command, command))
		}
		return result.output, result.err
	}

	out, err := osexec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		err = fmt.Errorf("%s failed: '%s %s': %v", cmd, cmd, strings.Join(args, " "), err)
	}
	return string(out), err
}
