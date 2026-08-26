# Renamed Same Package Import

This test case validates that imports from the same package are correctly handled when both files have package rename annotations.

## Scenario

The test simulates a real-world scenario based on the example baremetal instance types API:

1. Two proto files in the same package (`example.private.v1`):
   - `instance_type.proto` - defines the `InstanceType` message
   - `instance_service.proto` - defines a service that uses `InstanceType`

2. Both files have the same package rename annotation:
   - `option (cleanapi.file).package = "example.public.v1";`

3. The service file imports the type file:
   - Input: `import "example/private/v1/instance_type.proto";`
   - Expected output: `import "example/public/v1/instance_type.proto";`

## What This Tests

This test case specifically validates the `reverseRenameImportPath` and `isImportUsed` functions in `internal/generator/generator.go`.

When checking if an import is still used:
1. The import path in the processed content has been renamed (e.g., `example/public/v1/instance_type.proto`)
2. The original file descriptor is stored under the original path (e.g., `example/private/v1/instance_type.proto`)
3. The code must reverse the rename to find the original file descriptor and check if the imported types are still referenced

Without this fix, the generator might incorrectly:
- Remove the import because it can't find the file descriptor under the renamed path
- Or fail to properly validate that the import is still needed

## Expected Behavior

The generated output should:
- ✅ Rename the package declaration from `example.private.v1` to `example.public.v1`
- ✅ Rename the import path from `example/private/v1/instance_type.proto` to `example/public/v1/instance_type.proto`
- ✅ Keep the import because `InstanceType` is used in the service messages
- ✅ Remove private messages (like `InstanceTypesCreateRequest` and `InstanceTypesCreateResponse`)
- ✅ Remove the cleanapi import
- ✅ Keep the google/api/annotations import (used in HTTP options)
