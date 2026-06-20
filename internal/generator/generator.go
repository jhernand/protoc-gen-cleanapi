/*
Copyright (c) 2026 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
the License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.
*/

package generator

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/jhernand/protoc-gen-cleanapi/internal/api/cleanapi"
)

// Builder contains the data and logic needed to create a new public API generator. Don't create instances of this
// directly, use the New function instead.
type Builder struct {
	logger *slog.Logger
}

// Object is the actual public API generator.
type Object struct {
	logger           *slog.Logger
	protoRoot        string
	packageRenames   map[string]string
	privateFileNames map[string]bool
}

// New creates a new generator builder.
func New() *Builder {
	return &Builder{}
}

// SetLogger sets the logger for the generator. This is mandatory.
func (b *Builder) SetLogger(logger *slog.Logger) *Builder {
	b.logger = logger
	return b
}

// Build uses the data stored in the builder to create a new public API generator.
func (b *Builder) Build() (result *Object, err error) {
	// Check parameters:
	if b.logger == nil {
		err = fmt.Errorf("logger is mandatory")
		return
	}

	// Create and populate the object:
	result = &Object{
		logger: b.logger,
	}
	return
}

// Generate processes the code generator request and produces the modified proto files.
func (o *Object) Generate(request *pluginpb.CodeGeneratorRequest) (response *pluginpb.CodeGeneratorResponse, err error) {
	// Create an initially empty response:
	response = &pluginpb.CodeGeneratorResponse{
		SupportedFeatures: proto.Uint64(uint64(
			pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL,
		)),
	}

	// Parse parameters from the generator request.
	if request.Parameter != nil {
		o.parseParameters(*request.Parameter)
	}

	// Check that the mandatory 'proto_root' parameter is set.
	if o.protoRoot == "" {
		err = fmt.Errorf("mandatory parameter 'proto_root' is not set")
		return
	}

	// Initialize the discovered package mappings map.
	o.packageRenames = make(map[string]string)

	// First pass: discover all package mappings and collect private file names.
	o.privateFileNames = make(map[string]bool)
	for _, file := range request.ProtoFile {
		if o.isFilePrivate(file) {
			o.privateFileNames[file.GetName()] = true
		}
		packageOverride := o.getPackageName(file)
		if packageOverride != "" {
			o.packageRenames[file.GetPackage()] = packageOverride
			o.logger.Debug(
				"Discovered package mapping",
				slog.String("original", file.GetPackage()),
				slog.String("override", packageOverride),
			)
		}
	}

	// Process each file to generate.
	for _, file := range request.FileToGenerate {
		// Find the file descriptor::
		o.logger.Debug(
			"Finding file descriptor",
			slog.String("file", file),
		)
		desc := o.findFile(request.ProtoFile, file)
		if desc == nil {
			err = fmt.Errorf("file not found: %s", file)
			return
		}

		// Skip the file if it is marked as private:
		if o.isFilePrivate(desc) {
			o.logger.Debug(
				"Skipping private file",
				slog.String("file", file),
			)
			continue
		}

		// Process the content of the file:
		var content string
		content, err = o.processContent(desc, file)
		if err != nil {
			err = fmt.Errorf("failed to process content of file '%s': %w", file, err)
			return
		}

		// Create output file.
		output := o.getOutputFileName(desc, file)
		response.File = append(response.File, &pluginpb.CodeGeneratorResponse_File{
			Name:    proto.String(output),
			Content: proto.String(content),
		})
	}

	return
}

// parseParameters extracts configuration from the plugin parameters.
func (o *Object) parseParameters(parameters string) error {
	pairs := strings.Split(parameters, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid parameter format '%s'", pair)
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		switch key {
		case "proto_root":
			o.protoRoot = value
		default:
			return fmt.Errorf("unknown parameter '%s'", key)
		}
	}
	return nil
}

// findFile locates a file descriptor by name.
func (o *Object) findFile(descs []*descriptorpb.FileDescriptorProto, name string) *descriptorpb.FileDescriptorProto {
	for _, desc := range descs {
		if desc.GetName() == name {
			return desc
		}
	}
	return nil
}

// getOutputFileName returns the output filename for the public API. If the file has a package override annotation and
// the file path matches the package structure, the output path is adjusted to match the new package structure.
func (o *Object) getOutputFileName(file *descriptorpb.FileDescriptorProto, inputName string) string {
	// If there is no custom package name then use the original file name without any changes:
	customPackage := o.getPackageName(file)
	if customPackage == "" {
		return inputName
	}

	// Convert the original and custom package names to directory paths.
	originalPackage := file.GetPackage()
	originalPath := strings.Replace(originalPackage, ".", "/", -1)
	customPath := strings.Replace(customPackage, ".", "/", -1)

	// Replace the package-based directory structure in the file path. For example:
	//
	// events/v1/events.proto -> public_events/v1/events.proto
	outputName := inputName
	if strings.HasPrefix(inputName, originalPath+"/") {
		outputName = strings.Replace(inputName, originalPath+"/", customPath+"/", 1)
		o.logger.Debug(
			"Transformed output filename",
			slog.String("input", inputName),
			slog.String("output", outputName),
		)
	}

	return outputName
}

// processContent processes a proto file and removes private elements.
func (o *Object) processContent(desc *descriptorpb.FileDescriptorProto, file string) (result string, err error) {
	// Read the original file content.
	content, err := o.getContent(file)
	if err != nil {
		err = fmt.Errorf("failed to read original file %s: %w", file, err)
		return
	}

	// Get the source code info.
	info := desc.GetSourceCodeInfo()
	if info == nil {
		err = fmt.Errorf("no source code info available for file '%s'", file)
		return
	}

	// Find all private elements and their line ranges.
	linesToRemove := o.findPrivateLines(desc, info)

	// Remove private lines:
	content = o.removeLines(content, linesToRemove)

	// Apply package renaming.
	content, err = o.renamePackages(content, desc)
	if err != nil {
		err = fmt.Errorf("failed to rename packages in file '%s': %w", file, err)
		return
	}

	// Remove import of 'cleanapi.proto' since all private options are removed.
	content = o.removePrivateOptionsImport(content)

	// Remove imports that reference private files.
	content = o.removePrivateFileImports(content, desc)

	// Remove HTTP transcoding options if requested:
	if o.shouldRemoveHttpOptions(desc) {
		content = o.removeHttpOptions(content)
	}

	// Format the output (always enabled).
	content = o.formatContent(content)

	// Return the modified content:
	result = content
	return
}

// getContent reads the original proto file from the filesystem.
func (o *Object) getContent(file string) (result string, err error) {
	path := filepath.Join(o.protoRoot, file)
	data, err := os.ReadFile(path)
	if err != nil {
		err = fmt.Errorf("failed to read file '%s' from path '%s': %w", file, path, err)
		return
	}
	result = string(data)
	return
}

// findPrivateLines identifies line ranges that should be removed.
func (o *Object) findPrivateLines(file *descriptorpb.FileDescriptorProto,
	sourceInfo *descriptorpb.SourceCodeInfo) map[int]bool {

	// Create a logger with details aobut the file:
	logger := o.logger.With(
		slog.String("package", file.GetPackage()),
		slog.String("file", file.GetName()),
	)

	// Start with an empty set of lines to remove:
	linesToRemove := make(map[int]bool)

	// Check messages:
	for i, msg := range file.MessageType {
		logger.Debug(
			"Checking message",
			slog.String("message", msg.GetName()),
		)
		if o.isMessagePrivate(msg) {
			path := []int32{
				messageTag,
				int32(i),
			}
			o.addLinesFromPath(sourceInfo, path, linesToRemove)
		} else {
			// Check fields within the message.
			for j, field := range msg.Field {
				if o.isFieldPrivate(field) {
					path := []int32{4, int32(i), 2, int32(j)} // 2 is field number for field.
					o.addLinesFromPath(sourceInfo, path, linesToRemove)
				}
			}
			// Check nested messages.
			for j, nested := range msg.NestedType {
				if o.isMessagePrivate(nested) {
					path := []int32{
						messageTag,
						int32(i),
						nestedTag,
						int32(j),
					}
					o.addLinesFromPath(sourceInfo, path, linesToRemove)
				}
			}
		}
	}

	// Check enums:
	for i, enum := range file.EnumType {
		logger.Debug(
			"Checking enum",
			slog.String("enum", enum.GetName()),
		)
		if o.isEnumPrivate(enum) {
			path := []int32{enumTag, int32(i)}
			o.addLinesFromPath(sourceInfo, path, linesToRemove)
		} else {
			// Check enum values.
			for j, value := range enum.Value {
				logger.Debug(
					"Checking enum value",
					slog.String("enum", enum.GetName()),
					slog.String("value", value.GetName()),
				)
				if o.isEnumValuePrivate(value) {
					path := []int32{
						enumTag,
						int32(i),
						valueTag,
						int32(j),
					}
					o.addLinesFromPath(sourceInfo, path, linesToRemove)
				}
			}
		}
	}

	// Check services:
	for i, service := range file.Service {
		logger.Debug(
			"Checking service",
			slog.String("service", service.GetName()),
		)
		if o.isServicePrivate(service) {
			path := []int32{
				serviceTag,
				int32(i),
			}
			o.addLinesFromPath(sourceInfo, path, linesToRemove)
		} else {
			// Check methods within the service.
			for j, method := range service.Method {
				logger.Debug(
					"Checking method",
					slog.String("service", service.GetName()),
					slog.String("method", method.GetName()),
				)
				if o.isMethodPrivate(method) {
					path := []int32{
						serviceTag,
						int32(i),
						methodTag,
						int32(j),
					}
					o.addLinesFromPath(sourceInfo, path, linesToRemove)
				}
			}
		}
	}

	return linesToRemove
}

// addLinesFromPath adds all lines from a source location to the removal set.
// This includes the element itself and any leading/trailing comments.
func (o *Object) addLinesFromPath(sourceInfo *descriptorpb.SourceCodeInfo, path []int32,
	linesToRemove map[int]bool) {
	for _, loc := range sourceInfo.Location {
		if o.pathsEqual(loc.Path, path) {
			if len(loc.Span) >= 3 {
				startLine := int(loc.Span[0])
				var endLine int

				if len(loc.Span) == 3 {
					// Single-line span: [line, startColumn, endColumn].
					endLine = startLine
				} else {
					// Multi-line span: [startLine, startColumn, endLine, endColumn].
					endLine = int(loc.Span[2])
				}

				// The span already includes leading comments in protobuf's SourceCodeInfo. We also need to check
				// for leading_detached_comments which might be on earlier lines.
				if len(loc.LeadingDetachedComments) > 0 {
					// Count the total lines in all detached comment blocks.
					for _, commentBlock := range loc.LeadingDetachedComments {
						lines := strings.Count(commentBlock, "\n")
						// Detached comments are before leading comments, so we extend backwards.
						startLine -= (lines + 1) // +1 for the blank line separator.
					}
				}

				// Check if there's a leading comment that extends before the span.
				if loc.LeadingComments != nil && *loc.LeadingComments != "" {
					// Count lines in leading comment.
					commentLines := strings.Count(*loc.LeadingComments, "\n")
					if commentLines > 0 {
						// Leading comments are included in the span, but we need to make sure we're capturing
						// them all.
						startLine -= commentLines
					}
				}

				// Also remove any trailing blank lines after the element. This helps clean up formatting.
				if loc.TrailingComments != nil && *loc.TrailingComments != "" {
					commentLines := strings.Count(*loc.TrailingComments, "\n")
					endLine += commentLines
				}

				for line := startLine; line <= endLine; line++ {
					linesToRemove[line] = true
				}
			}
			break
		}
	}
}

// pathsEqual checks if two paths are equal.
func (o *Object) pathsEqual(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (o *Object) isMessagePrivate(msg *descriptorpb.DescriptorProto) bool {
	if msg.Options == nil {
		return false
	}
	result := o.hasPrivateOption(msg.Options)
	if result {
		o.logger.Debug(
			"Message is private",
			slog.String("message", msg.GetName()),
		)
	}
	return result
}

func (o *Object) isFieldPrivate(field *descriptorpb.FieldDescriptorProto) bool {
	if field.Options == nil {
		return false
	}
	result := o.hasPrivateOption(field.GetOptions())
	if result {
		o.logger.Debug(
			"Field is private",
			slog.String("field", field.GetName()),
		)
	}
	return o.hasPrivateOption(field.GetOptions())
}

func (o *Object) isEnumPrivate(enum *descriptorpb.EnumDescriptorProto) bool {
	if enum.Options == nil {
		return false
	}
	result := o.hasPrivateOption(enum.GetOptions())
	if result {
		o.logger.Debug(
			"Enum is private",
			slog.String("enum", enum.GetName()),
		)
	}
	return result
}

func (o *Object) isEnumValuePrivate(value *descriptorpb.EnumValueDescriptorProto) bool {
	if value.Options == nil {
		return false
	}
	result := o.hasPrivateOption(value.Options)
	if result {
		o.logger.Debug(
			"Enum value is private",
			slog.String("enum value", value.GetName()),
		)
	}
	return result
}

func (o *Object) isServicePrivate(service *descriptorpb.ServiceDescriptorProto) bool {
	if service.Options == nil {
		return false
	}
	result := o.hasPrivateOption(service.GetOptions())
	if result {
		o.logger.Debug(
			"Service is private",
			slog.String("service", service.GetName()),
		)
	}
	return result
}

func (o *Object) isMethodPrivate(method *descriptorpb.MethodDescriptorProto) bool {
	if method.Options == nil {
		return false
	}
	result := o.hasPrivateOption(method.Options)
	if result {
		o.logger.Debug(
			"Method is private",
			slog.String("method", method.GetName()),
		)
	}
	return o.hasPrivateOption(method.Options)
}

func (o *Object) isFilePrivate(file *descriptorpb.FileDescriptorProto) bool {
	if file.Options == nil {
		return false
	}
	result := o.hasPrivateOption(file.Options)
	if result {
		o.logger.Debug(
			"File is private",
			slog.String("file", file.GetName()),
		)
	}
	return result
}

// getPackageName extracts the package override from the file-level annotation. Returns an empty string if no
// override is specified.
func (o *Object) getPackageName(file *descriptorpb.FileDescriptorProto) string {
	if file.Options == nil {
		return ""
	}
	extension := proto.GetExtension(file.Options, cleanapi.E_File)
	if extension != nil {
		fileOptions, ok := extension.(*cleanapi.FileOptions)
		if ok {
			packageOverride := fileOptions.GetPackage()
			if packageOverride != "" {
				o.logger.Debug(
					"Found package override in annotation",
					slog.String("file", file.GetName()),
					slog.String("original", file.GetPackage()),
					slog.String("override", packageOverride),
				)
				return packageOverride
			}
		}
	}
	return ""
}

// shouldRemoveHttpOptions checks if HTTP transcoding options should be removed based on the file-level annotation.
func (o *Object) shouldRemoveHttpOptions(file *descriptorpb.FileDescriptorProto) bool {
	if file.Options == nil {
		return false
	}
	extension := proto.GetExtension(file.Options, cleanapi.E_File)
	if extension != nil {
		fileOptions, ok := extension.(*cleanapi.FileOptions)
		if ok {
			if fileOptions.GetRemoveHttpOptions() {
				o.logger.Debug(
					"HTTP transcoding options removal enabled",
					slog.String("file", file.GetName()),
				)
				return true
			}
		}
	}
	return false
}

// hasPrivateOption checks if a proto message has the private extension set to true. This method uses type
// assertions to determine which type of options message is passed and then accesses the appropriate extension.
func (o *Object) hasPrivateOption(options proto.Message) bool {
	if options == nil {
		return false
	}
	switch options := options.(type) {
	case *descriptorpb.FileOptions:
		extension := proto.GetExtension(options, cleanapi.E_File)
		if extension != nil {
			options, ok := extension.(*cleanapi.FileOptions)
			if ok {
				return options.GetPrivate()
			}
		}
	case *descriptorpb.MessageOptions:
		extension := proto.GetExtension(options, cleanapi.E_Message)
		if extension != nil {
			options, ok := extension.(*cleanapi.MessageOptions)
			if ok {
				return options.GetPrivate()
			}
		}
	case *descriptorpb.FieldOptions:
		extension := proto.GetExtension(options, cleanapi.E_Field)
		if extension != nil {
			options, ok := extension.(*cleanapi.FieldOptions)
			if ok {
				return options.GetPrivate()
			}
		}
	case *descriptorpb.EnumOptions:
		extension := proto.GetExtension(options, cleanapi.E_Enum)
		if extension != nil {
			options, ok := extension.(*cleanapi.EnumOptions)
			if ok {
				return options.GetPrivate()
			}
		}
	case *descriptorpb.EnumValueOptions:
		extension := proto.GetExtension(options, cleanapi.E_Value)
		if extension != nil {
			options, ok := extension.(*cleanapi.EnumValueOptions)
			if ok {
				return options.GetPrivate()
			}
		}
	case *descriptorpb.ServiceOptions:
		extension := proto.GetExtension(options, cleanapi.E_Service)
		if extension != nil {
			options, ok := extension.(*cleanapi.ServiceOptions)
			if ok {
				return options.GetPrivate()
			}
		}
	case *descriptorpb.MethodOptions:
		extension := proto.GetExtension(options, cleanapi.E_Method)
		if extension != nil {
			options, ok := extension.(*cleanapi.MethodOptions)
			if ok {
				return options.GetPrivate()
			}
		}
	}
	return false
}

// removeLines removes specified lines from the content.
func (o *Object) removeLines(content string, linesToRemove map[int]bool) string {
	if len(linesToRemove) == 0 {
		return content
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	var result bytes.Buffer
	lineNum := 0

	for scanner.Scan() {
		if !linesToRemove[lineNum] {
			result.WriteString(scanner.Text())
			result.WriteString("\n")
		}
		lineNum++
	}

	return result.String()
}

// removePrivateOptionsImport removes the import statement for 'cleanapi.proto' and file-level options since all
// annotations are removed in the generated output.
func (o *Object) removePrivateOptionsImport(content string) string {
	lines := strings.Split(content, "\n")
	var result []string

	for _, line := range lines {
		// Skip lines that import 'cleanapi.proto':
		if strings.Contains(line, "cleanapi/cleanapi.proto") {
			continue
		}
		// Skip file-level option lines for the package annotation:
		if strings.Contains(line, "option (cleanapi.file)") {
			continue
		}
		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

// removePrivateFileImports removes import statements that reference files marked as private. It uses the file
// descriptor's dependency list to match import lines against the collected set of private file names.
func (o *Object) removePrivateFileImports(content string, desc *descriptorpb.FileDescriptorProto) string {
	privateDeps := make(map[string]bool)
	for _, dep := range desc.Dependency {
		if o.privateFileNames[dep] {
			privateDeps[dep] = true
			o.logger.Debug(
				"Removing import of private file",
				slog.String("file", desc.GetName()),
				slog.String("import", dep),
			)
		}
	}
	if len(privateDeps) == 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		skip := false
		for dep := range privateDeps {
			if strings.Contains(line, fmt.Sprintf(`"%s"`, dep)) {
				skip = true
				break
			}
		}
		if !skip {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// removeHttpOptions removes google.api.http option blocks from the content. These can be single-line or multi-line
// blocks. Also removes the import of google/api/annotations.proto.
func (o *Object) removeHttpOptions(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inHttpOption := false
	braceDepth := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip google/api/annotations.proto import.
		if strings.Contains(trimmed, "google/api/annotations.proto") {
			continue
		}

		// Check if we're starting an HTTP option block.
		if strings.Contains(trimmed, "option (google.api.http)") {
			inHttpOption = true
			braceDepth = 0
			// Count braces on this line.
			for _, ch := range trimmed {
				switch ch {
				case '{':
					braceDepth++
				case '}':
					braceDepth--
				}
			}
			// Check if the option ends on the same line (e.g., single-line format).
			if strings.HasSuffix(trimmed, ";") || braceDepth == 0 {
				inHttpOption = false
			}
			continue
		}

		// If we're in an HTTP option block, skip lines and track braces.
		if inHttpOption {
			for _, ch := range trimmed {
				switch ch {
				case '{':
					braceDepth++
				case '}':
					braceDepth--
				}
			}
			// If we've closed all braces and found the semicolon, we're done with this block.
			if braceDepth <= 0 && strings.HasSuffix(trimmed, ";") {
				inHttpOption = false
			}
			continue
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

// renamePackages applies package name transformations based on the file-level annotation. If no package override is
// specified in the annotation, the package name is left unchanged. Also renames cross-package references.
func (o *Object) renamePackages(content string, file *descriptorpb.FileDescriptorProto) (result string, err error) {
	// Get the original package name from the file descriptor:
	originalPackage := file.GetPackage()

	// If there is a custom package name in the file annotation, then rename the package declaration:
	customPackage := o.getPackageName(file)
	if customPackage != "" {
		pattern := fmt.Sprintf(`package\s+%s\s*;`, originalPackage)
		var regex *regexp.Regexp
		regex, err = regexp.Compile(pattern)
		if err != nil {
			return
		}
		replacement := fmt.Sprintf("package %s;", customPackage)
		content = regex.ReplaceAllString(content, replacement)
	}

	// Apply to import paths and fully qualified type references the package renamings that have been discovered:
	for oldPkg, newPkg := range o.packageRenames {
		// Update import paths:
		oldPath := strings.Replace(oldPkg, ".", "/", -1)
		newPath := strings.Replace(newPkg, ".", "/", -1)
		content = strings.ReplaceAll(content, fmt.Sprintf(`"%s/`, oldPath), fmt.Sprintf(`"%s/`, newPath))

		// Update fully qualified type references:
		content = strings.ReplaceAll(content, oldPkg+".", newPkg+".")
	}

	// Return the updated content:
	result = content
	return
}

// formatContent applies basic formatting to clean up the proto file. This removes excessive blank lines and normalizes
// whitespace.
func (o *Object) formatContent(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	var prevBlank bool

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		isBlank := trimmed == ""

		// Skip consecutive blank lines.
		if isBlank && prevBlank {
			continue
		}

		// Skip blank line immediately after opening curly bracket.
		if isBlank && i > 0 {
			prevTrimmed := strings.TrimSpace(lines[i-1])
			if strings.HasSuffix(prevTrimmed, "{") {
				continue
			}
		}

		// Skip blank line immediately before closing curly bracket.
		if isBlank && i < len(lines)-1 {
			nextTrimmed := strings.TrimSpace(lines[i+1])
			if nextTrimmed == "}" || strings.HasPrefix(nextTrimmed, "}") {
				continue
			}
		}

		result = append(result, line)
		prevBlank = isBlank
	}

	// Remove trailing blank lines.
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}

	// Ensure file ends with a single newline.
	output := strings.Join(result, "\n")
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}

	return output
}

// Tags for various fields using in the paths used to access source code information. Thse can be found in
// the 'descriptor.proto' source code. For example, as of this writing, the tag for the 'message_type' field
// is 4, and it is defined here:
//
// https://github.com/protocolbuffers/protobuf/blob/v33.0/src/google/protobuf/descriptor.proto#L121
const (
	enumTag    = 5
	messageTag = 4
	methodTag  = 2
	nestedTag  = 3
	serviceTag = 6
	valueTag   = 2
)
