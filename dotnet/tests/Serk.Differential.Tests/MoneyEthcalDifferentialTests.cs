using System.Text.Json;

namespace Serk.Differential.Tests;

/// <summary>
/// Differential test of pkg/money and pkg/ethcal (Go, the behavioural reference) against
/// Serk.Platform.Numerics and Serk.Platform.Primitives (C#). Both run on the same generated cases; every
/// result must agree, or be explained by a documented deviation that recomputes Go's answer; and every
/// documented deviation must occur, so the list cannot go stale.
/// </summary>
public class MoneyEthcalDifferentialTests
{
    private static readonly JsonSerializerOptions Indented = new() { WriteIndented = true };

    [Fact]
    public void Go_and_CSharp_agree_or_differ_only_as_documented()
    {
        var cases = MoneyEthcalCases.Build();
        var go = GoOracle.Run(cases);
        int agree = 0;
        var explained = Deviations.All.ToDictionary(d => d.Id, _ => new List<string>());
        var unexplained = new List<string>();
        var perOp = new SortedDictionary<string, int[]>(StringComparer.Ordinal); // agree, deviate
        foreach (var c in cases)
        {
            var g = go[c.Id];
            var cs = CSharpSide.Evaluate(c);
            if (!perOp.TryGetValue(c.Op, out var counts)) perOp[c.Op] = counts = new int[2];
            if (g == cs) { agree++; counts[0]++; continue; }
            counts[1]++;
            var d = Deviations.All.FirstOrDefault(x => x.Explains(c, g, cs));
            if (d is null) unexplained.Add(Deviations.Format(c, g, cs));
            else explained[d.Id].Add(Deviations.Format(c, g, cs));
        }

        var report = new
        {
            cases = cases.Count, agree, seed = MoneyEthcalCases.Seed,
            per_operation = perOp.ToDictionary(kv => kv.Key, kv => new { agree = kv.Value[0], deviate = kv.Value[1] }),
            deviations = explained.ToDictionary(kv => kv.Key, kv => new { count = kv.Value.Count, examples = kv.Value.Take(3) }),
            unexplained = unexplained.Take(20),
        };
        var json = JsonSerializer.Serialize(report, Indented);
        var outFile = Environment.GetEnvironmentVariable("SERK_DIFF_REPORT");
        if (!string.IsNullOrEmpty(outFile)) File.WriteAllText(outFile, json + "\n");

        Assert.True(unexplained.Count == 0, $"{unexplained.Count} unexplained differences:\n{string.Join("\n", unexplained.Take(20))}\n{json}");
        var unseen = explained.Where(kv => kv.Value.Count == 0).Select(kv => kv.Key).ToList();
        Assert.True(unseen.Count == 0, $"documented deviations never observed (stale or the case set lost them): {string.Join(", ", unseen)}");
        Assert.True(agree > cases.Count * 9 / 10, $"only {agree} of {cases.Count} cases agree");
    }
}
