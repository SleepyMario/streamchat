package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var lifecycleMu sync.Mutex

func powershell(ctx context.Context, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop';"+script)
	hideWindow(cmd)
	return cmd.Output()
}

const obsProcesses = `$p=@(Get-Process obs64 -ErrorAction SilentlyContinue | Where-Object {$_.Path -eq 'C:\Program Files\obs-studio\bin\64bit\obs64.exe'});`

func obsCount(ctx context.Context) (int, error) {
	b, e := powershell(ctx, obsProcesses+`Write-Output $p.Count`)
	if e != nil {
		return 0, e
	}
	var n int
	_, e = fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &n)
	return n, e
}
func (a *agent) lifecycle(w http.ResponseWriter, r *http.Request) {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	n, e := obsCount(ctx)
	if e != nil {
		http.Error(w, "Windows OBS process status unavailable", 503)
		return
	}
	if r.Method == "GET" {
		state, message := "stopped", "OBS is closed · ready to start"
		if n > 0 {
			state = "running"
			message = "OBS is running on Windows"
		}
		json.NewEncoder(w).Encode(map[string]string{"state": state, "message": message})
		return
	}
	switch r.URL.Path {
	case "/obs/start":
		if n == 0 {
			_, e = powershell(ctx, `Start-Process -FilePath 'C:\Program Files\obs-studio\bin\64bit\obs64.exe' -WorkingDirectory 'C:\Program Files\obs-studio\bin\64bit' -ArgumentList '--collection "Streamchat Main" --profile "Streamchat 1080p60" --scene "Starting Soon"'`)
		}
	case "/obs/stop":
		if n > 1 {
			http.Error(w, "Multiple OBS processes; close blocked", 409)
			return
		}
		if n == 1 {
			for _, method := range []string{"GetStreamStatus", "GetRecordStatus", "GetVirtualCamStatus"} {
				b, err := a.obs.request(ctx, method, nil)
				if err != nil {
					http.Error(w, "Cannot verify all OBS outputs idle; close blocked", 409)
					return
				}
				var result struct {
					Active *bool `json:"outputActive"`
				}
				if json.Unmarshal(b, &result) != nil || result.Active == nil || *result.Active {
					http.Error(w, "OBS output active or unknown; close blocked", 409)
					return
				}
			}

			b, err := a.obs.request(ctx, "GetOutputList", nil)
			var list struct {
				Outputs *[]struct {
					Active *bool `json:"outputActive"`
				} `json:"outputs"`
			}
			if err != nil || json.Unmarshal(b, &list) != nil || list.Outputs == nil {
				http.Error(w, "Cannot verify all outputs idle; close blocked", 409)
				return
			}
			for _, output := range *list.Outputs {
				if output.Active == nil || *output.Active {
					http.Error(w, "OBS output active or unknown; close blocked", 409)
					return
				}
			}
			_, e = powershell(ctx, obsProcesses+`if($p.Count -ne 1){throw 'OBS process changed'};if(-not $p[0].CloseMainWindow()){throw 'Normal close request rejected'}`)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if e != nil {
		http.Error(w, "Windows OBS command failed; refresh status before retrying", 502)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"message": "OBS request accepted; checking state"})
}
