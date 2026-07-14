package server

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
)

// knowledgebaseDir is the directory name within a project workspace that holds
// the OKF knowledge base.
const knowledgebaseDir = ".horde"

// kbDirPerm is the permission for knowledgebase directories.
const kbDirPerm = 0o755

// kbFilePerm is the permission for knowledgebase files.
const kbFilePerm = 0o644

// kbSubDirs are the OKF category directories created for every new project.
var kbSubDirs = []string{
	"knowledgebase",
	"knowledgebase/concepts",
	"knowledgebase/decisions",
	"knowledgebase/patterns",
	"knowledgebase/plans",
	"knowledgebase/references",
}

// kbIndexTemplate is the seed content for a new project's knowledgebase
// index.md. It follows the OKF v0.1 frontmatter convention.
const kbIndexTemplate = `---
okf_version: "0.1"
---

# %s knowledge base

This is the working knowledge base for the %s project, conforming to the
[Open Knowledge Format (OKF) v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).

It consolidates working knowledge: what the project is, how it is structured,
decisions and their rationale, recurring patterns, and plans. It is authored
by people and agents and meant to be read by both.

## Concepts

*(Add concept docs here as the project evolves.)*

## Decisions

*(Add decision docs here as architectural choices are made.)*

## Patterns

*(Add pattern docs here as conventions emerge.)*

## Plans

*(Add plan docs here as work is scoped.)*

## References

*(Add reference docs here for external specs and standards.)*
`

// kbLogTemplate is the seed content for a new project's knowledgebase log.md.
const kbLogTemplate = `# Knowledge Base Update Log

## %s
* **Initialization**: Created the knowledge base structure conforming to OKF v0.1.
`

// kbCategoryIndex is the seed content for each category index.md.
const kbCategoryIndex = `# %s

Records of %s.
`

// scaffoldKnowledgebase creates the `.horde/knowledgebase/` directory
// structure within the project's workspace, seeded with index.md, log.md, and
// category index files. It is called when a project is created. If the
// workspace directory does not exist, it is created.
//
// This does not implement synchronization — that is a future phase. The
// knowledgebase is local to the workspace and travels with it (e.g. checked
// into git if the workspace is a repo).
func scaffoldKnowledgebase(workspace, projectName string) error {
	hordeDir := filepath.Join(workspace, knowledgebaseDir)
	if err := os.MkdirAll(hordeDir, kbDirPerm); err != nil {
		return fmt.Errorf("create .horde dir: %w", err)
	}

	for _, sub := range kbSubDirs {
		dir := filepath.Join(hordeDir, sub)
		if err := os.MkdirAll(dir, kbDirPerm); err != nil {
			return fmt.Errorf("create kb subdir %s: %w", sub, err)
		}
	}

	kbDir := filepath.Join(hordeDir, "knowledgebase")
	date := time.Now().UTC().Format("2006-01-02")

	// index.md
	indexPath := filepath.Join(kbDir, "index.md")
	if err := writeIfNotExists(indexPath, []byte(fmt.Sprintf(kbIndexTemplate, projectName, projectName))); err != nil {
		return fmt.Errorf("write kb index.md: %w", err)
	}

	// log.md
	logPath := filepath.Join(kbDir, "log.md")
	if err := writeIfNotExists(logPath, []byte(fmt.Sprintf(kbLogTemplate, date))); err != nil {
		return fmt.Errorf("write kb log.md: %w", err)
	}

	// Category index files
	categories := []string{"concepts", "decisions", "patterns", "plans", "references"}
	for _, cat := range categories {
		catPath := filepath.Join(kbDir, cat, "index.md")
		title := titleCase(cat)
		content := fmt.Sprintf(kbCategoryIndex, title, title)
		if err := writeIfNotExists(catPath, []byte(content)); err != nil {
			return fmt.Errorf("write kb %s/index.md: %w", cat, err)
		}
	}

	logrus.WithFields(logrus.Fields{
		"workspace": workspace, logKeyProject: projectName,
	}).Debug("scaffolded knowledgebase")

	return nil
}

// writeIfNotExists writes data to path only if the file does not already exist.
// This prevents overwriting a knowledgebase that was already created (e.g. on
// re-running project creation against an existing workspace).
func writeIfNotExists(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil // file exists; don't overwrite
	}
	return os.WriteFile(path, data, kbFilePerm)
}

// asciiUpperOffset is the difference between a lowercase ASCII letter and
// its uppercase counterpart (used to uppercase the first letter of a word).
const asciiUpperOffset = 32

// titleCase returns s with the first letter uppercased.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-asciiUpperOffset) + s[1:]
}
