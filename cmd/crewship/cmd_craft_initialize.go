package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// craftInitializeApplication maps --application to the server's application ID.
func craftInitializeApplication(cmd *cobra.Command) (string, error) {
	name, _ := cmd.Flags().GetString("application")
	switch name {
	case "", "word", "wordcraft":
		return "wordcraft", nil
	case "sheet", "gridcraft":
		return "gridcraft", nil
	}
	return "", cli.WithExitCode(errors.New(`--application must be "word" or "sheet"`), cli.ExitValidation)
}

func craftInitialize(cmd *cobra.Command, args []string) error {
	application, err := craftInitializeApplication(cmd)
	if err != nil {
		return err
	}
	name, err := cmd.Flags().GetString("file")
	if err != nil || name == "" {
		return fmt.Errorf("native initialization requires --file")
	}
	input := cmd.InOrStdin()
	if name != "-" {
		f, e := os.Open(name)
		if e != nil {
			return fmt.Errorf("cannot read native document file")
		}
		defer f.Close()
		input = f
	}
	native, err := io.ReadAll(io.LimitReader(input, craftNativeBytesLimit+1))
	if err != nil || len(native) == 0 || len(native) > craftNativeBytesLimit {
		return fmt.Errorf("native document must contain 1 to32MiB of native bytes")
	}
	c, err := pageClient()
	if err != nil {
		return err
	}
	c = c.WithContext(cmd.Context())
	body := struct {
		Application string `json:"application_id" yaml:"application_id"`
		Bytes       []byte `json:"bytes" yaml:"bytes"`
	}{application, native}
	response, err := c.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/craft/initialize", body)
	if err != nil {
		return craftTransportError("native initialization request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 201 {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return craftHTTPError(response, "")
		}
		return fmt.Errorf("native initialization HTTP %d", response.StatusCode)
	}
	return craftUseAdmission(cmd, args, c, response, application)
}
