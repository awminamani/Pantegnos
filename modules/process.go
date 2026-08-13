package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Process decrypts the given config file contents and returns the decoded text.
// filename is used only for extension/protocol detection (e.g. "config.npvt").
//
// It mirrors the dispatch logic that used to live in main.go, but instead of
// scanning a directory and writing .txt files, it operates on in-memory bytes
// and returns the decrypted string. File-path-based modules (e.g. .npvt) still
// receive a real temp file on disk because their decoder reads from a path.
func Process(filename string, content []byte) (string, error) {
	contentStr := strings.TrimSpace(string(content))
	fileExt := strings.ToLower(filepath.Ext(filename))

	// Stage the bytes in a temp file so modules that read from a path work.
	tmp, err := os.MkdirTemp("", "pantegnos-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	outDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", err
	}

	inPath := filepath.Join(tmp, "input"+fileExt)
	if err := os.WriteFile(inPath, []byte(contentStr), 0600); err != nil {
		return "", err
	}

	var protocol, payload string
	moduleFound := false

	// 1) Match by extension.
	for _, module := range Registry {
		if module.Extension == fileExt {
			if strings.Contains(contentStr, "://") {
				parts := strings.SplitN(contentStr, "://", 2)
				protocol = parts[0]
				payload = parts[1]
			} else {
				protocol = ""
				payload = contentStr
			}
			module.Exec(protocol, payload, fileExt, inPath, outDir)
			moduleFound = true
			break
		}
	}

	// 2) Fall back to matching by protocol prefix/suffix.
	if !moduleFound {
		if !strings.Contains(contentStr, "://") {
			return "", fmt.Errorf("no module matches extension %q and input has no protocol separator", fileExt)
		}
		parts := strings.SplitN(contentStr, "://", 2)
		protocol = parts[0]
		payload = parts[1]

		for _, module := range Registry {
			matchFound := false
			for _, protoPattern := range module.Proto {
				if strings.HasSuffix(protoPattern, "*") {
					prefix := strings.TrimSuffix(protoPattern, "*")
					if strings.HasPrefix(protocol, prefix) {
						matchFound = true
						break
					}
				} else if protocol == protoPattern {
					matchFound = true
					break
				}
			}
			if matchFound {
				module.Exec(protocol, payload, fileExt, inPath, outDir)
				moduleFound = true
				break
			}
		}
	}

	if !moduleFound {
		return "", fmt.Errorf("no module found for file %q", filename)
	}

	// Every module writes its output to <basename-without-ext>.txt inside outDir.
	// Because we named the staged file "input<ext>", the output is always
	// "input.txt".
	outPath := filepath.Join(outDir, "input.txt")
	data, err := os.ReadFile(outPath)
	if err != nil {
		return "", fmt.Errorf("decryption produced no output (the config may require interactive input, e.g. a SlipNet password bundle): %w", err)
	}
	return string(data), nil
}
