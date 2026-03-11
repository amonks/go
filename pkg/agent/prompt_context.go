package agent

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

const (
	workflowContextTemplateName    = "workflow-context.tmpl"
	reviewQuestionsTemplateName    = "review-questions.tmpl"
	reviewInstructionsTemplateName = "review-instructions.tmpl"
)

//go:embed templates/*.tmpl
var defaultTemplates embed.FS

// promptContextFromRepo loads project-level context templates, context files, and test commands.
// testCommands is a fallback; if the caller's prompt already has test commands, those take precedence.
func promptContextFromRepo(workDir, globalConfigDir string, testCommands []string) (PromptContent, error) {
	workflowContext, err := readProjectPromptTemplate(workDir, workflowContextTemplateName)
	if err != nil {
		return PromptContent{}, err
	}
	workflowContext, err = renderContextTemplate("workflow_context", workflowContext)
	if err != nil {
		return PromptContent{}, err
	}
	workflowContext = "\n" + workflowContext + "\n"
	reviewQuestions, err := readProjectPromptTemplate(workDir, reviewQuestionsTemplateName)
	if err != nil {
		return PromptContent{}, err
	}
	reviewQuestions, err = renderContextTemplate("review_questions", reviewQuestions)
	if err != nil {
		return PromptContent{}, err
	}
	reviewQuestions += "\n"
	contextFiles, err := loadContextFileContents(workDir, globalConfigDir)
	if err != nil {
		return PromptContent{}, err
	}
	reviewInstructions, err := readDefaultPromptTemplate(reviewInstructionsTemplateName)
	if err != nil {
		return PromptContent{}, err
	}

	content := PromptContent{
		ProjectContext: []string{workflowContext, reviewQuestions, reviewInstructions},
		ContextFiles:   contextFiles,
		TestCommands:   testCommands,
	}
	return content, nil
}

func readProjectPromptTemplate(workDir, name string) (string, error) {
	if workDir == "" {
		workDir = "."
	}
	overridePath := filepath.Join(workDir, ".incrementum", "templates", name)
	if data, err := os.ReadFile(overridePath); err == nil {
		return string(data), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read prompt override: %w", err)
	}
	return readDefaultPromptTemplate(name)
}

func readDefaultPromptTemplate(name string) (string, error) {
	data, err := defaultTemplates.ReadFile(filepath.Join("templates", name))
	if err != nil {
		return "", fmt.Errorf("read default prompt: %w", err)
	}
	return string(data), nil
}

// loadContextFileContents loads AGENTS.md or CLAUDE.md contents using LoadContextFiles.
func loadContextFileContents(workDir, globalConfigDir string) ([]string, error) {
	files, err := LoadContextFiles(LoadContextFilesOptions{WorkDir: workDir, GlobalConfigDir: globalConfigDir})
	if err != nil {
		return nil, err
	}
	var contents []string
	for _, f := range files {
		trimmed := strings.TrimRight(f.Content, "\r\n")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		contents = append(contents, trimmed)
	}
	return contents, nil
}

func renderContextTemplate(name, contents string) (string, error) {
	tmpl, err := template.New("context").Option("missingkey=error").Parse(contents)
	if err != nil {
		return "", fmt.Errorf("parse %s template: %w", name, err)
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, name, nil); err != nil {
		return "", fmt.Errorf("render %s template: %w", name, err)
	}
	return strings.TrimSpace(out.String()), nil
}
