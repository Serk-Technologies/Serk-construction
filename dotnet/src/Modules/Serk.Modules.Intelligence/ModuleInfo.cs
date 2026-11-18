namespace Serk.Modules.Intelligence;

/// <summary>
/// The Intelligence module's identity and migration state. Nothing of the Go module has been migrated here yet: see
/// dotnet/docs/MIGRATION_TRACEABILITY.md for its requirements, tables and routes. The Go implementation stays
/// authoritative until a module passes its differential and acceptance tests and the owner approves cutover.
/// </summary>
public static class ModuleInfo
{
    public const string Name = "Intelligence";
    public const string Owner = "Data and AI lead";
    public const string MigrationStatus = "NOT_STARTED";
}
