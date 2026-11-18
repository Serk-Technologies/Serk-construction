namespace Serk.Platform.Numerics;

/// <summary>
/// Calculation provenance: what was computed, from which canonical inputs, under which rounding policies and
/// kernel version, with which result, and the SHA-256 of all of it. A recorded calculation can be re-run and
/// compared byte for byte; it is evidence, not an approval.
/// </summary>
public sealed record CalculationRecord(string Operation, string KernelVersion, string InputsCanonical, IReadOnlyList<string> Policies, string ResultCanonical, string Sha256)
{
    public const string Kernel = "serk-numerics/1";

    public static CalculationRecord Of(string operation, IReadOnlyDictionary<string, object?> inputs, IEnumerable<RoundingPolicy> policies, object? result)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(operation);
        var inCanon = Canonical.Serialize(inputs);
        var pol = policies.Select(p => p.Key).Order(StringComparer.Ordinal).ToArray();
        var outCanon = Canonical.Serialize(result);
        var body = Canonical.Serialize(new Dictionary<string, object?>
        {
            ["operation"] = operation, ["kernel"] = Kernel, ["inputs"] = inCanon, ["policies"] = pol, ["result"] = outCanon,
        });
        return new CalculationRecord(operation, Kernel, inCanon, pol, outCanon, Canonical.Sha256Hex(body));
    }

    /// <summary>Runs a calculation under a compute budget and records it.</summary>
    public static (T Result, CalculationRecord Record) Run<T>(string operation, IReadOnlyDictionary<string, object?> inputs, IEnumerable<RoundingPolicy> policies,
        Func<T> compute, int maxBits = 4096, long maxOperations = 1_000_000)
    {
        T result;
        using (ComputeBudget.Begin(maxBits, maxOperations)) result = compute();
        return (result, Of(operation, inputs, policies, result));
    }
}
