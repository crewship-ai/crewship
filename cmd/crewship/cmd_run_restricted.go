package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
)

func restrictedChatProfile(client *cli.Client, chat string) (bool, error) {
	return restrictedExecutionProfile(client, "/api/v1/chats/"+chat+"/execution-profile")
}
func restrictedAgentRunProfile(client *cli.Client, agent string) (bool, error) {
	return restrictedExecutionProfile(client, "/api/v1/agents/"+agent+"/run-profile")
}
func restrictedExecutionProfile(client *cli.Client, path string) (bool, error) {
	resp, err := client.Get(path)
	if err != nil {
		return false, err
	}
	if err = cli.CheckError(resp); err != nil {
		return false, err
	}
	var profile struct {
		Mode string `json:"mode" yaml:"mode"`
	}
	if err = cli.ReadJSON(resp, &profile); err != nil {
		return false, err
	}
	switch profile.Mode {
	case "restricted":
		return true, nil
	case "trusted":
		return false, nil
	default:
		return false, fmt.Errorf("unknown execution profile")
	}
}
func runRestrictedText(client *cli.Client, chat, input string, md *cli.MarkdownRenderer, save *cli.AtomicFile, noStream bool, versions []string) error {
	format := cli.NewFormatter(cli.ResolveFormat(flagFormat, cliCfg))
	var result strings.Builder
	resp, err := client.Post("/api/v1/chats/"+chat+"/restricted-cli-run", map[string]any{"content": input, "project_file_versions": versions})
	if err != nil {
		return err
	}
	if err = cli.CheckError(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), 256<<10)
	done := false
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type string `json:"type" yaml:"type"`
			Text string `json:"text" yaml:"text"`
		}
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return err
		}
		switch event.Type {
		case "text":
			if done {
				return fmt.Errorf("late restricted output")
			}
			result.WriteString(event.Text)
			if save != nil {
				if _, err = save.WriteString(event.Text); err != nil {
					return err
				}
			}
			if format.RoutesToHuman() && !noStream {
				if md != nil {
					fmt.Print(md.Write(event.Text))
				} else {
					fmt.Print(event.Text)
				}
			}
		case "done":
			done = true
		default:
			return fmt.Errorf("invalid restricted event")
		}
	}
	if err = scan.Err(); err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("restricted response incomplete or revoked")
	}
	if save != nil {
		if err = save.Commit(); err != nil {
			return err
		}
	}
	return format.AutoHuman(map[string]string{"chat_id": chat, "text": result.String(), "status": "completed"}, func() {
		if noStream {
			if md != nil {
				fmt.Print(md.Render(result.String()))
			} else {
				fmt.Print(result.String())
			}
		} else if md != nil {
			fmt.Print(md.Flush())
		}
		fmt.Println()
	})
}
