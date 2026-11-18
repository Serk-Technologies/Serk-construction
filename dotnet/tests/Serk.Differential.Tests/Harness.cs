using System.Diagnostics;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace Serk.Differential.Tests;

/// <summary>One differential case: an operation and its arguments, in the oracle's JSON shape.</summary>
public sealed record Case(string Id, string Op, Dictionary<string, object> Args);

/// <summary>A canonical outcome: a value (string or list of strings) or a coarse error class.</summary>
public sealed record Outcome(bool Ok, string? Value, string? Error)
{
    public static Outcome Of(string v) => new(true, v, null);
    public static Outcome Fail(string error) => new(false, null, error);
    public override string ToString() => Ok ? $"ok {Value}" : $"error {Error}";
}

/// <summary>Runs the Go reference implementation (dotnet/differential/go-oracle) on a case set.</summary>
public static class GoOracle
{
    public static string RepoRoot()
    {
        for (var d = new DirectoryInfo(AppContext.BaseDirectory); d is not null; d = d.Parent)
            if (File.Exists(Path.Combine(d.FullName, "go.mod")) && Directory.Exists(Path.Combine(d.FullName, "dotnet"))) return d.FullName;
        throw new InvalidOperationException("the Go repository root (go.mod) was not found above the test binaries");
    }

    public static string CasesJson(IEnumerable<Case> cases) => JsonSerializer.Serialize(cases.Select(c =>
    {
        var o = new Dictionary<string, object> { ["id"] = c.Id, ["op"] = c.Op };
        foreach (var (k, v) in c.Args) o[k] = v;
        return o;
    }));

    /// <summary>Runs the oracle; a missing Go toolchain fails the test (never a skip).</summary>
    public static Dictionary<string, Outcome> Run(IReadOnlyList<Case> cases)
    {
        var root = RepoRoot();
        var file = Path.Combine(Path.GetTempPath(), $"serk-diff-{Guid.NewGuid():N}.json");
        File.WriteAllText(file, CasesJson(cases));
        var keep = Environment.GetEnvironmentVariable("SERK_DIFF_KEEP_CASES");
        if (!string.IsNullOrEmpty(keep)) File.Copy(file, keep, overwrite: true);
        try
        {
            var psi = new ProcessStartInfo(Environment.GetEnvironmentVariable("SERK_GO") ?? "go", ["run", "./dotnet/differential/go-oracle", file])
            {
                WorkingDirectory = root, RedirectStandardOutput = true, RedirectStandardError = true,
            };
            using var p = Process.Start(psi) ?? throw new InvalidOperationException("could not start the Go toolchain");
            var stdout = p.StandardOutput.ReadToEndAsync();
            var stderr = p.StandardError.ReadToEndAsync();
            var limit = TimeSpan.FromSeconds(int.TryParse(Environment.GetEnvironmentVariable("SERK_DIFF_TIMEOUT_S"), out var t) ? t : 300);
            if (!p.WaitForExit(limit)) { p.Kill(true); throw new TimeoutException($"the Go oracle did not finish in {limit.TotalSeconds} s"); }
            if (p.ExitCode != 0) throw new InvalidOperationException($"the Go oracle failed ({p.ExitCode}): {stderr.Result}");
            var arr = JsonNode.Parse(stdout.Result)!.AsArray();
            var map = new Dictionary<string, Outcome>();
            foreach (var n in arr)
            {
                var id = n!["id"]!.GetValue<string>();
                if (n["ok"]!.GetValue<bool>())
                {
                    var v = n["value"]!;
                    map[id] = Outcome.Of(v is JsonArray a ? string.Join("|", a.Select(x => x!.GetValue<string>())) : v.GetValue<string>());
                }
                else map[id] = Outcome.Fail(n["error"]!.GetValue<string>());
            }
            if (map.Count != cases.Count) throw new InvalidOperationException($"the oracle answered {map.Count} of {cases.Count} cases");
            return map;
        }
        finally { File.Delete(file); }
    }
}
