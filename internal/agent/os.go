package agent

import "runtime"

func currentOS() string {
    return runtime.GOOS
}

func JobMatchesOS(jobOS string) bool {
    if jobOS == "" || jobOS == "all" {
        return true
    }
    return jobOS == currentOS()
}