# Dependency review

Every package is pinned in `Directory.Packages.props`, locked (`packages.lock.json`), restored from nuget.org only
(`nuget.config` source mapping), and justified here. A package is not adopted because the directive names it.

| Package | Version | Used by | Why | Reviewed |
|---|---|---|---|---|
| (none) | | Serk.Platform.Numerics, Serk.Platform.Primitives | The kernel needs only System.Numerics.BigInteger; an architecture test forbids packages there | 2026-11-18 |
| Npgsql | 10.0.3 | Serk.Platform.Persistence, persistence tests | The PostgreSQL driver for .NET (MIT); explicit SQL, no ORM | 2026-11-18 |
| xunit | 2.9.3 | tests | Mature xUnit 2 line; the v3 4.x line was not adopted yet, pending a separate upgrade review | 2026-11-18 |
| xunit.runner.visualstudio | 3.1.5 | tests | Runner for xUnit 2 under `dotnet test` | 2026-11-18 |
| Microsoft.NET.Test.Sdk | 18.12.0 | tests | Test host | 2026-11-18 |
| FsCheck.Xunit | 3.6.0 | kernel tests | Property-based tests (BSD-3) | 2026-11-18 |

Not adopted yet, to be reviewed when the first use appears:
- EF Core: only for mapping onto the existing schema, never for creating it.
- Blazor, Avalonia, MAUI, SignalR, gRPC.
- OpenTelemetry.
- Microsoft.Extensions.AI and ML.NET/ONNX.
- Testcontainers. Today's tests use a disposable local PostgreSQL named by an environment variable.
- Playwright for .NET.
- xBIM: licence (CDDL) review needed.
