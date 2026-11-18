using System.Text.RegularExpressions;
using System.Xml.Linq;

namespace Serk.Architecture.Tests;

/// <summary>
/// Module boundaries (ADR 0026), read from the .csproj files and sources, so a violation fails the build's
/// tests rather than a review:
/// <list type="bullet">
/// <item>platform projects never reference a module;</item>
/// <item>a module references platform projects and other modules' published <c>.Api</c> projects only;</item>
/// <item>the project graph has no cycle;</item>
/// <item>the numerical kernel uses no third-party package, no System.Decimal and no Math.Round;</item>
/// <item>every module of the directive exists and names its owner.</item>
/// </list>
/// </summary>
public class BoundaryTests
{
    private static readonly string Root = FindRoot();
    private static readonly string[] DirectiveModules =
        ["Command", "Estimate", "Plan", "Design", "Supply", "Stock", "Fleet", "People", "Contracts", "Capital", "Assure", "Connect", "Intelligence", "Governance"];

    private static string FindRoot()
    {
        for (var d = new DirectoryInfo(AppContext.BaseDirectory); d is not null; d = d.Parent)
            if (File.Exists(Path.Combine(d.FullName, "SerkOS.slnx"))) return d.FullName;
        throw new InvalidOperationException("SerkOS.slnx not found");
    }

    private sealed record Proj(string Name, string Path, IReadOnlyList<string> Refs, IReadOnlyList<string> Packages);

    private static List<Proj> Projects() => Directory.EnumerateFiles(Path.Combine(Root, "src"), "*.csproj", SearchOption.AllDirectories)
        .Select(p =>
        {
            var x = XDocument.Load(p);
            var refs = x.Descendants("ProjectReference").Select(e => System.IO.Path.GetFileNameWithoutExtension(e.Attribute("Include")!.Value)).ToList();
            var pkgs = x.Descendants("PackageReference").Select(e => e.Attribute("Include")!.Value).ToList();
            return new Proj(System.IO.Path.GetFileNameWithoutExtension(p), p, refs, pkgs);
        }).ToList();

    [Fact]
    public void Platform_never_depends_on_a_module()
    {
        var bad = Projects().Where(p => p.Name.StartsWith("Serk.Platform.")).SelectMany(p => p.Refs.Where(r => r.StartsWith("Serk.Modules.")).Select(r => $"{p.Name} -> {r}")).ToList();
        Assert.True(bad.Count == 0, string.Join("\n", bad));
    }

    [Fact]
    public void Modules_meet_only_through_published_Api_projects()
    {
        var bad = new List<string>();
        foreach (var p in Projects().Where(p => p.Name.StartsWith("Serk.Modules.")))
        {
            var own = p.Name.EndsWith(".Api") ? p.Name[..^4] : p.Name;
            foreach (var r in p.Refs)
            {
                if (r.StartsWith("Serk.Platform.")) continue;
                if (r.StartsWith("Serk.Modules.") && r.EndsWith(".Api") && r[..^4] != own) continue;
                if (r == own + ".Api" && !p.Name.EndsWith(".Api")) continue; // a module implements its own Api
                bad.Add($"{p.Name} -> {r}");
            }
            if (p.Name.EndsWith(".Api") && p.Refs.Any(r => !r.StartsWith("Serk.Platform."))) bad.Add($"{p.Name}: an Api project depends on platform types only");
        }
        Assert.True(bad.Count == 0, "forbidden references:\n" + string.Join("\n", bad));
    }

    [Fact]
    public void The_project_graph_has_no_cycle()
    {
        var graph = Projects().ToDictionary(p => p.Name, p => p.Refs);
        var state = new Dictionary<string, int>();
        var cycle = new List<string>();
        bool Visit(string n, Stack<string> path)
        {
            if (state.GetValueOrDefault(n) == 2) return false;
            if (state.GetValueOrDefault(n) == 1) { cycle.AddRange(path.Reverse().SkipWhile(x => x != n).Append(n)); return true; }
            state[n] = 1;
            path.Push(n);
            foreach (var m in graph.GetValueOrDefault(n, [])) if (Visit(m, path)) return true;
            path.Pop();
            state[n] = 2;
            return false;
        }
        foreach (var n in graph.Keys) if (Visit(n, new Stack<string>())) break;
        Assert.True(cycle.Count == 0, "cycle: " + string.Join(" -> ", cycle));
    }

    [Fact]
    public void The_numerical_kernel_is_self_contained_and_never_uses_decimal_or_floating_rounding()
    {
        var kernel = Projects().Single(p => p.Name == "Serk.Platform.Numerics");
        Assert.Empty(kernel.Packages);
        Assert.Empty(kernel.Refs);
        var dir = System.IO.Path.GetDirectoryName(kernel.Path)!;
        var bad = new List<string>();
        foreach (var f in Directory.EnumerateFiles(dir, "*.cs", SearchOption.AllDirectories).Where(f => !f.Contains("/obj/") && !f.Contains("/bin/")))
        {
            var code = Regex.Replace(File.ReadAllText(f), @"//.*|/\*[\s\S]*?\*/|""(?:\\.|[^""\\])*""", "");
            foreach (Match m in Regex.Matches(code, @"\bdecimal\b|\bSystem\.Decimal\b|\bMath\.Round\b|\bMathF?\.Floor\b|\bfloat\b"))
                bad.Add($"{System.IO.Path.GetFileName(f)}: {m.Value}");
        }
        Assert.True(bad.Count == 0, string.Join("\n", bad));
    }

    [Fact]
    public void Every_directive_module_exists_and_names_its_owner()
    {
        foreach (var m in DirectiveModules)
        {
            var info = System.IO.Path.Combine(Root, "src", "Modules", $"Serk.Modules.{m}", "ModuleInfo.cs");
            Assert.True(File.Exists(info), $"module {m} is missing");
            var text = File.ReadAllText(info);
            Assert.Matches(@"Owner = ""[^""]+""", text);
            Assert.Matches(@"MigrationStatus = ""(NOT_STARTED|IN_PROGRESS|DIFFERENTIAL_PASS|MIGRATED)""", text);
        }
    }
}
