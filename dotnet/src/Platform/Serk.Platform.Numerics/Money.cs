using System.Numerics;

namespace Serk.Platform.Numerics;

/// <summary>
/// An exact amount of one currency, always held at the currency's minor unit. Arithmetic between amounts of
/// different currencies is an error. Multiplication and division compute the exact rational result and then
/// take one rounding step under a named <see cref="RoundingPolicy"/>: there is no intermediate rounding
/// (pkg/money's Div rounds to 16 places first; see DIFF-001 in dotnet/docs/DIFFERENTIAL.md).
/// </summary>
public readonly record struct Money
{
    public Rational Amount { get; }
    public Currency Currency { get; }

    private Money(Rational amount, Currency currency)
    {
        Amount = amount;
        Currency = currency;
    }

    /// <summary>Rounds an exact value to the currency's minor unit with <paramref name="mode"/>.</summary>
    public static Money Of(Rational value, Currency currency, RoundingMode mode = RoundingMode.HalfEven) =>
        new(value.Round(currency.MinorUnits, mode), currency);

    public static Money Zero(Currency currency) => new(Rational.Zero, currency);

    public static Money FromMinor(BigInteger minor, Currency currency) => new(Rational.Create(minor, BigInteger.Pow(10, currency.MinorUnits)), currency);

    public BigInteger MinorUnits => (Amount * Rational.Pow10(Currency.MinorUnits)).Numerator;

    /// <summary>
    /// The only text form accepted for amounts: the kernel's strict decimal grammar (no sign on zero, no
    /// leading zeros, no exponent), at most 18 integer digits and at most min(6, minor units) decimals.
    /// </summary>
    public static Money Parse(string text, Currency currency)
    {
        var r = StrictGrammar.ParseDecimal(text);
        int dot = text.IndexOf('.');
        int intDigits = (dot < 0 ? text.Length : dot) - (text.StartsWith('-') ? 1 : 0);
        int decimals = dot < 0 ? 0 : text.Length - dot - 1;
        if (intDigits > 18) throw new FormatException($"\"{text}\" has more than 18 integer digits");
        if (decimals > Math.Min(6, currency.MinorUnits)) throw new FormatException($"\"{text}\" has more decimals than {currency.Code} allows ({currency.MinorUnits})");
        return new(r, currency);
    }

    private void Same(Money other)
    {
        if (!ReferenceEquals(Currency, other.Currency) && Currency.Code != other.Currency.Code)
            throw new CurrencyMismatchException(Currency.Code, other.Currency.Code);
    }

    public static Money operator +(Money a, Money b) { a.Same(b); return new(a.Amount + b.Amount, a.Currency); }
    public static Money operator -(Money a, Money b) { a.Same(b); return new(a.Amount - b.Amount, a.Currency); }
    public static Money operator -(Money a) => new(-a.Amount, a.Currency);
    public Money Abs() => new(Amount.Abs(), Currency);
    public int CompareTo(Money other) { Same(other); return Amount.CompareTo(other.Amount); }

    public Money Multiply(Rational factor, RoundingMode mode) => Of(Amount * factor, Currency, mode);

    public Money Divide(Rational divisor, RoundingMode mode)
    {
        if (divisor.IsZero) throw new DivideByZeroException("money: division by zero");
        return Of(Amount / divisor, Currency, mode);
    }

    /// <summary>
    /// Splits the amount by non-negative integer ratios with no unit lost or created: each part gets its floor
    /// share of minor units and the leftovers go to the largest remainders, earlier parts first on ties
    /// (pkg/money.Allocate's rule, without its 64-bit limits; see DIFF-002).
    /// </summary>
    public IReadOnlyList<Money> Allocate(IReadOnlyList<BigInteger> ratios)
    {
        if (ratios.Count == 0) throw new ArgumentException("no allocation ratios", nameof(ratios));
        BigInteger total = BigInteger.Zero;
        foreach (var r in ratios)
        {
            if (r.Sign < 0) throw new ArgumentException("negative allocation ratio", nameof(ratios));
            total += r;
        }
        if (total.IsZero) throw new ArgumentException("allocation ratios sum to zero", nameof(ratios));
        var whole = MinorUnits;
        int sign = whole.Sign < 0 ? -1 : 1;
        whole = BigInteger.Abs(whole);
        var parts = new BigInteger[ratios.Count];
        var rem = new BigInteger[ratios.Count];
        var used = new bool[ratios.Count];
        BigInteger distributed = BigInteger.Zero;
        for (int i = 0; i < ratios.Count; i++)
        {
            parts[i] = BigInteger.DivRem(whole * ratios[i], total, out rem[i]);
            distributed += parts[i];
        }
        for (var left = whole - distributed; left > 0; left--)
        {
            int best = -1;
            for (int i = 0; i < rem.Length; i++)
                if (!used[i] && (best == -1 || rem[i] > rem[best])) best = i;
            parts[best]++;
            used[best] = true;
        }
        var cur = Currency;
        return parts.Select(p => FromMinor(sign * p, cur)).ToArray();
    }

    /// <summary>Fixed text at the minor unit, e.g. "1234.50".</summary>
    public string AmountText => Amount.ToFixedString(Currency.MinorUnits);
    public override string ToString() => $"{AmountText} {Currency.Code}";
}

public sealed class CurrencyMismatchException(string a, string b) : Exception($"currency mismatch: {a} vs {b}");
