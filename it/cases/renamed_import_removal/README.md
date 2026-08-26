# Renamed Import Removal

This test case validates that imports from the same package are correctly removed when the imported types are only used in private elements that get removed during generation.

## Scenario

The test simulates a scenario where:

1. Two proto files in the same package (`example.private.v1`):
   - `metadata_type.proto` - defines the `Metadata` message
   - `instance_type.proto` - defines `InstanceType` that uses `Metadata` only in a private field

2. Both files have the same package rename annotation:
   - `option (cleanapi.file).package = "example.public.v1";`

3. The instance type file imports the metadata type:
   - Input: `import "example/private/v1/metadata_type.proto";`
   - Expected output: `import "example/public/v1/metadata_type.proto";`

4. The `Metadata` field is marked as private and gets removed:
   - Input: `Metadata metadata = 3 [(cleanapi.field).private = true];`
   - Expected: Field is removed from output

## What This Tests

This test case validates the complement to the `renamed_same_package_import` test. It ensures that:

1. **Renamed imports are removed when unused**: After package renaming, if the imported types are no longer referenced (because they were only used in private fields), the import is correctly removed

2. **The reverse rename logic works for removal**: The `reverseRenameImportPath` and `isImportUsed` functions correctly:
   - Reverse the import path to find the original file descriptor
   - Check if any types from that file are still used
   - Remove the import when no types are referenced

Without this working correctly, the generator might:
- ❌ Keep an unused import in the output
- ❌ Generate invalid proto files that import types that aren't used

## Expected Behavior

The generated `instance_type.proto` should:
- ✅ Rename the package declaration from `example.private.v1` to `example.public.v1`
- ✅ Remove the private `metadata` field (which used the `Metadata` type)
- ✅ **Remove the import of `metadata_type.proto`** - even though comments mention "Metadata", the import removal logic strips comments before checking type usage
- ✅ Remove the cleanapi import
- ✅ Keep the remaining public fields

This demonstrates that comments are correctly ignored when determining if an import is used - only actual type references in the proto syntax matter.

## Contrast with renamed_same_package_import

| Test Case | Import Path | Import Used? | Expected |
|-----------|-------------|--------------|----------|
| `renamed_same_package_import` | `example/private/v1/instance_type.proto` → `example/public/v1/instance_type.proto` | Yes (in public fields) | **KEPT** |
| `renamed_import_removal` | `example/private/v1/metadata_type.proto` → (would be) `example/public/v1/metadata_type.proto` | No (only in private field) | **REMOVED** |

Both tests validate the same underlying code path (`isImportUsed` with renamed packages) but with opposite outcomes.
