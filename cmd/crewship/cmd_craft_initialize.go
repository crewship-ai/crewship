package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

func craftInitialize(cmd *cobra.Command, args []string) error {
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
	}{"wordcraft", native}
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
	return craftUseAdmission(cmd, args, c, response, true)
}
