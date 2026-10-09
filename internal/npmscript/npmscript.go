// Package npmscript reads package.json scripts as npm runs them: commands
// split on && and ;, words, leading environment assignments and runners,
// and `npm run` (pnpm, yarn, bun) into other scripts.
package npmscript

import "strings"

// Command is one command of a script: its words, without the leading
// environment assignments and runners, and that environment.
type Command struct {
	Args []string
	Env  map[string]string
}

// Commands expands script name into its commands, in order, following
// `npm run` style commands into the scripts they name (once each).
func Commands(scripts map[string]string, name string) []Command {
	var out []Command
	seen := map[string]bool{}
	var expand func(string)
	expand = func(name string) {
		script, ok := scripts[name]
		if !ok || seen[name] {
			return
		}
		seen[name] = true
		for _, cmd := range SplitCommands(script) {
			args, env := StripEnv(ShellWords(cmd))
			if len(args) == 0 {
				continue
			}
			if target := RunTarget(args, scripts); target != "" {
				expand(target)
				continue
			}
			out = append(out, Command{Args: args, Env: env})
		}
	}
	expand(name)
	return out
}

// RunTarget is the script an `npm run X` style command runs, or "".
func RunTarget(args []string, scripts map[string]string) string {
	switch args[0] {
	case "npm", "bun":
		if len(args) > 2 && (args[1] == "run" || args[1] == "run-script") {
			return args[2]
		}
	case "pnpm", "yarn":
		if len(args) > 2 && args[1] == "run" {
			return args[2]
		}
		if len(args) > 1 {
			if _, ok := scripts[args[1]]; ok {
				return args[1]
			}
		}
	}
	return ""
}

// StripEnv removes leading environment assignments (A=B, cross-env A=B)
// and runners (npx, pnpm exec), returning the command and the environment.
func StripEnv(args []string) ([]string, map[string]string) {
	env := map[string]string{}
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "cross-env" || a == "npx" || a == "env":
			args = args[1:]
		case (a == "pnpm" || a == "yarn" || a == "bun") && len(args) > 1 && (args[1] == "exec" || args[1] == "x"):
			args = args[2:]
		case strings.Contains(a, "=") && !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "="):
			k, v, _ := strings.Cut(a, "=")
			env[k] = v
			args = args[1:]
		default:
			return args, env
		}
	}
	return args, env
}

// SplitCommands splits a script on && and ; (outside quotes).
func SplitCommands(script string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ';' || (c == '&' && i+1 < len(script) && script[i+1] == '&'):
			out = append(out, cur.String())
			cur.Reset()
			if c == '&' {
				i++
			}
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, cur.String())
}

// ShellWords splits a command into words, honoring simple quotes.
func ShellWords(cmd string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	in := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
