using System.Globalization;
using System.Numerics;
using Serk.Platform.Numerics;

namespace Serk.Differential.Tests;

/// <summary>
/// The documented differences between pkg/money / pkg/ethcal and the C# kernel (dotnet/docs/DIFFERENTIAL.md).
/// A mismatch is accepted only when one of these explains it — by recomputing Go's mechanism and getting
/// Go's exact answer — never by a blanket allowance. Any other mismatch fails the test.
/// </summary>
public static class Deviations
{
    public sealed record Deviation(string Id, string Title, Func<Case, Outcome, Outcome, bool> Explains);

    public static readonly IReadOnlyList<Deviation> All =
    [
        new("DIFF-001", "pkg/money Div rounds to 16 places (half away from zero) before the currency rounding; the C# kernel rounds the exact quotient once",
            (c, go, cs) => c.Op == "money.div" && go.Ok && cs.Ok && go.Value == GoDoubleRoundedDivision(c)),
        new("DIFF-002", "pkg/money Allocate uses 64-bit integers: past 2^63 minor units (amount × ratio) it silently returns wrapped-around amounts or loops for ~2^63 steps; the C# kernel is exact",
            (c, go, cs) => c.Op == "money.allocate" && cs.Ok && GoInt64Allocation(c) is var (value, steps)
                && ((go.Ok && go.Value == value) || (go.Error == "nontermination" && steps > 100_000_000))),
        new("DIFF-003", "pkg/ethcal Parse (fmt.Sscanf) accepts non-canonical spellings such as a signed year; the C# parser accepts only YYYY-MM-DD digits",
            (c, go, cs) => c.Op == "eth.parse" && go.Ok && !cs.Ok && go.Value!.Split(' ')[0] != CSharpSide.S(c, "text")),
        new("DIFF-004", "pkg/money ParseStrict accepts leading zeros and a signed zero (\"007.50\", \"-0.00\"); the C# grammar has one spelling per value",
            (c, go, cs) => c.Op == "money.parse_strict" && go.Ok && !cs.Ok && NonCanonicalButNumeric(CSharpSide.S(c, "amount"))),
        new("DIFF-005", "Gregorian dates after 9999-12-31 (Ethiopian years 9992 to 9999): Go's time.Time represents them; System.DateOnly ends at 9999-12-31",
            (c, go, cs) => c.Op == "eth.to_gregorian" && go.Ok && cs.Error == "out_of_range" && CSharpSide.I(c, "y") >= 9992),
    ];

    /// <summary>pkg/money.Money.Div: DivRound(divisor, 16) — half away from zero — then ApplyRounding at the minor unit.</summary>
    private static string GoDoubleRoundedDivision(Case c)
    {
        var cur = Currency.Of(CSharpSide.S(c, "currency"));
        var exact = Rational.Parse(CSharpSide.S(c, "amount")) / Rational.Parse(CSharpSide.S(c, "factor"));
        var mode = CSharpSide.Mode(CSharpSide.S(c, "policy"))!.Value;
        return exact.Round(16, RoundingMode.HalfUp).Round(cur.MinorUnits, mode).ToFixedString(cur.MinorUnits);
    }

    /// <summary>
    /// pkg/money.Money.Allocate with Go's int64 semantics (big.Int.Int64() keeps the low 64 bits; products wrap),
    /// in closed form: the leftover loop gives one unit to each largest remainder, then — once every remainder is
    /// the first maximum is already −1 — keeps choosing that index. Returns Go's parts and how many loop steps Go takes to produce them.
    /// </summary>
    private static (string Value, BigInteger Steps) GoInt64Allocation(Case c)
    {
        var cur = Currency.Of(CSharpSide.S(c, "currency"));
        var ratios = CSharpSide.Ratios(c);
        var minor = (Rational.Parse(CSharpSide.S(c, "amount")) * Rational.Pow10(cur.MinorUnits)).Numerator;
        long whole = unchecked((long)(ulong)(BigInteger.Abs(minor) & ulong.MaxValue));
        if (minor.Sign < 0) whole = unchecked(-whole);
        long total = 0;
        foreach (var r in ratios) total = unchecked(total + r);
        long sign = 1;
        if (whole < 0) { sign = -1; whole = unchecked(-whole); }
        var parts = new BigInteger[ratios.Length];
        var rem = new long[ratios.Length];
        long distributed = 0;
        for (int i = 0; i < ratios.Length; i++)
        {
            long p = unchecked(whole * ratios[i]) / total;
            parts[i] = p;
            rem[i] = unchecked(whole * ratios[i]) % total;
            distributed = unchecked(distributed + p);
        }
        long left = unchecked(whole - distributed);
        BigInteger steps = Math.Max(left, 0);
        while (left > 0)
        {
            int best = -1;
            for (int i = 0; i < rem.Length; i++) if (best == -1 || rem[i] > rem[best]) best = i;
            if (rem[best] == -1)
            {
                parts[best] += left; // the same index wins every remaining step
                break;
            }
            parts[best]++;
            rem[best] = -1;
            left--;
        }
        string Fmt(BigInteger p) => Rational.Create(unchecked((long)(ulong)((sign * p) & ulong.MaxValue)), BigInteger.Pow(10, cur.MinorUnits)).ToFixedString(cur.MinorUnits);
        return (string.Join("|", parts.Select(Fmt)), steps);
    }

    private static bool NonCanonicalButNumeric(string s) =>
        System.Text.RegularExpressions.Regex.IsMatch(s, @"^-?[0-9]{1,18}(\.[0-9]{1,6})?$")
        && (System.Text.RegularExpressions.Regex.IsMatch(s, @"^-?0[0-9]") || System.Text.RegularExpressions.Regex.IsMatch(s, @"^-0(\.0+)?$"));

    public static string Format(Case c, Outcome go, Outcome cs) =>
        string.Create(CultureInfo.InvariantCulture, $"{c.Id} {CasesText(c)}: Go {go}, C# {cs}");

    private static string CasesText(Case c) => string.Join(" ", c.Args.Select(kv => $"{kv.Key}={(kv.Value is long[] a ? "[" + string.Join(",", a) + "]" : kv.Value)}"));
}
