// Package tools checks for external CLI tools on PATH without running them.
package tools

import "os/exec"

// Tool is the presence of one external tool.
type Tool struct {
	Name  string
	Found bool
	Path  string
	Hint  string
}

// Tofu returns "tofu" if found, else "terraform" if found.
func Tofu() (string, bool) {
	if _, err := exec.LookPath("tofu"); err == nil {
		return "tofu", true
	}
	if _, err := exec.LookPath("terraform"); err == nil {
		return "terraform", true
	}
	return "tofu", false
}

func lookup(name, hint string) Tool {
	p, err := exec.LookPath(name)
	return Tool{Name: name, Found: err == nil, Path: p, Hint: hint}
}

// Check reports tofu (or terraform), gh, docker and ssh-keygen.
func Check() []Tool {
	const tofuHint = "https://opentofu.org/docs/intro/install/"
	name, _ := Tofu()
	return []Tool{
		lookup(name, tofuHint),
		lookup("gh", "https://cli.github.com"),
		lookup("docker", "https://docs.docker.com/get-docker/"),
		lookup("ssh-keygen", "install OpenSSH"),
	}
}
