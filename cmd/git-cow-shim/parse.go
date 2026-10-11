package main

import "strings"

// rewrite accepts only syntax whose checkout behavior the COW command supports.
// Git itself still parses all arguments, including cumulative -C and -c options.
// Unknown or incomplete syntax is passed through before starting any command.
func rewrite(args []string, bypass bool) ([]string, string) {
	pass := func(reason string) ([]string, string) { return args, reason }
	if bypass {
		return pass("bypass")
	}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-C" || a == "-c" {
			if i+1 >= len(args) || args[i+1] == "" {
				return pass("incomplete global option")
			}
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-C") && len(a) > 2 || strings.HasPrefix(a, "-c") && len(a) > 2 {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			return pass("unsupported global option")
		}
		break
	}
	if i+1 >= len(args) || args[i] != "worktree" || args[i+1] != "add" {
		return pass("ordinary command")
	}
	command := i
	positional := 0
	separator := false
	checkout := true
	branch := false
	for i += 2; i < len(args); i++ {
		a := args[i]
		if separator {
			positional++
			continue
		}
		switch a {
		case "--":
			separator = true
		case "-h", "--help", "--orphan", "--no-orphan":
			return pass("help or orphan option")
		case "--no-checkout":
			checkout = false
		case "--checkout":
			checkout = true
		case "-b", "-B":
			if branch || i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return pass("incomplete or ambiguous branch option")
			}
			branch = true
			i++
		case "--detach", "-d", "--force", "-f", "--lock", "--guess-remote", "--no-guess-remote", "--quiet", "-q":
		case "--reason":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return pass("incomplete reason option")
			}
			i++
		default:
			if strings.HasPrefix(a, "-") {
				return pass("unsupported add option")
			}
			positional++
		}
	}
	if !checkout {
		return pass("no checkout")
	}
	if positional < 1 || positional > 2 {
		return pass("unsupported positional arguments")
	}
	out := append([]string(nil), args...)
	out[command] = "cow-worktree"
	return out, "cow"
}
