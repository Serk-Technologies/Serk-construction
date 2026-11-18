using System.Numerics;

namespace Serk.Platform.Numerics;

/// <summary>
/// Explicit precision and execution budget for exact arithmetic. Every rational produced is charged one
/// operation and checked against a maximum size in bits; exceeding either throws
/// <see cref="ComputeBudgetExceededException"/> instead of exhausting memory or time. The budget is ambient
/// per asynchronous flow: <c>using (ComputeBudget.Begin(maxBits, maxOperations)) { ... }</c>.
/// </summary>
public sealed class ComputeBudget : IDisposable
{
    /// <summary>Applies outside any explicit scope: generous, but finite.</summary>
    public const int DefaultMaxBits = 1 << 16;
    public const long DefaultMaxOperations = long.MaxValue;

    private static readonly AsyncLocal<ComputeBudget?> Ambient = new();
    private readonly ComputeBudget? _outer;

    public int MaxBits { get; }
    public long MaxOperations { get; }
    public long Operations { get; private set; }

    private ComputeBudget(int maxBits, long maxOperations, ComputeBudget? outer)
    {
        MaxBits = maxBits;
        MaxOperations = maxOperations;
        _outer = outer;
    }

    public static ComputeBudget Begin(int maxBits, long maxOperations)
    {
        ArgumentOutOfRangeException.ThrowIfLessThan(maxBits, 8);
        ArgumentOutOfRangeException.ThrowIfLessThan(maxOperations, 1);
        var b = new ComputeBudget(maxBits, maxOperations, Ambient.Value);
        Ambient.Value = b;
        return b;
    }

    public static ComputeBudget? Current => Ambient.Value;

    internal static void Charge(BigInteger num, BigInteger den)
    {
        var b = Ambient.Value;
        int maxBits = b?.MaxBits ?? DefaultMaxBits;
        long bits = Math.Max(BigInteger.Abs(num).GetBitLength(), den.GetBitLength());
        if (bits > maxBits) throw new ComputeBudgetExceededException($"a value of {bits} bits exceeds the precision budget of {maxBits} bits");
        if (b is null) return;
        b.Operations++;
        if (b.Operations > b.MaxOperations) throw new ComputeBudgetExceededException($"more than {b.MaxOperations} exact operations");
    }

    public void Dispose()
    {
        if (!ReferenceEquals(Ambient.Value, this)) throw new InvalidOperationException("compute budgets must be disposed innermost first");
        Ambient.Value = _outer;
    }
}

public sealed class ComputeBudgetExceededException(string message) : Exception(message);
