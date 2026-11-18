using System.Globalization;
using System.Numerics;
using System.Text;

namespace Serk.Platform.Numerics;

/// <summary>
/// An exact rational number: a BigInteger numerator over a positive BigInteger denominator, always reduced.
/// Immutable. Every arithmetic result is checked against the ambient <see cref="ComputeBudget"/>, so a
/// computation cannot grow without bound. Never converted to binary floating point implicitly.
/// </summary>
public readonly struct Rational : IEquatable<Rational>, IComparable<Rational>
{
    private readonly BigInteger _num;
    private readonly BigInteger _den; // 0 only in default(Rational), which is read as 0/1

    public static readonly Rational Zero = new(BigInteger.Zero, BigInteger.One, reduced: true);
    public static readonly Rational One = new(BigInteger.One, BigInteger.One, reduced: true);

    private Rational(BigInteger num, BigInteger den, bool reduced)
    {
        _num = num;
        _den = den;
        if (!reduced) throw new InvalidOperationException("use Create");
    }

    public BigInteger Numerator => _num;
    public BigInteger Denominator => _den.IsZero ? BigInteger.One : _den;
    public int Sign => _num.Sign;
    public bool IsZero => _num.IsZero;
    public bool IsInteger => Denominator.IsOne;

    /// <summary>numerator/denominator, reduced; throws on a zero denominator and on a budget overrun.</summary>
    public static Rational Create(BigInteger numerator, BigInteger denominator)
    {
        if (denominator.IsZero) throw new DivideByZeroException("rational with a zero denominator");
        if (denominator.Sign < 0) { numerator = -numerator; denominator = -denominator; }
        var g = BigInteger.GreatestCommonDivisor(numerator, denominator);
        if (!g.IsOne && !g.IsZero) { numerator /= g; denominator /= g; }
        if (numerator.IsZero) denominator = BigInteger.One;
        ComputeBudget.Charge(numerator, denominator);
        return new Rational(numerator, denominator, reduced: true);
    }

    public static Rational FromInteger(BigInteger value) => Create(value, BigInteger.One);
    public static implicit operator Rational(int value) => FromInteger(value);
    public static implicit operator Rational(long value) => FromInteger(value);

    /// <summary>The exact value of a finite IEEE 754 double (no decimal rounding); NaN and infinities are refused.</summary>
    public static Rational FromDoubleExact(double value)
    {
        if (!double.IsFinite(value)) throw new ArgumentOutOfRangeException(nameof(value), "not a finite number");
        if (value == 0) return Zero;
        long bits = BitConverter.DoubleToInt64Bits(value);
        bool negative = bits < 0;
        int exponent = (int)((bits >> 52) & 0x7FF);
        long mantissa = bits & 0xFFFFFFFFFFFFFL;
        if (exponent == 0) exponent++; else mantissa |= 1L << 52;
        exponent -= 1075;
        BigInteger m = negative ? -mantissa : mantissa;
        return exponent >= 0 ? FromInteger(m << exponent) : Create(m, BigInteger.One << -exponent);
    }

    public static Rational operator +(Rational a, Rational b) => Create(a._num * b.Denominator + b._num * a.Denominator, a.Denominator * b.Denominator);
    public static Rational operator -(Rational a, Rational b) => Create(a._num * b.Denominator - b._num * a.Denominator, a.Denominator * b.Denominator);
    public static Rational operator *(Rational a, Rational b) => Create(a._num * b._num, a.Denominator * b.Denominator);
    public static Rational operator /(Rational a, Rational b)
    {
        if (b.IsZero) throw new DivideByZeroException("division by zero");
        return Create(a._num * b.Denominator, a.Denominator * b._num);
    }
    public static Rational operator -(Rational a) => new(-a._num, a.Denominator, reduced: true);
    public Rational Abs() => _num.Sign < 0 ? -this : this;

    public static bool operator ==(Rational a, Rational b) => a.Equals(b);
    public static bool operator !=(Rational a, Rational b) => !a.Equals(b);
    public static bool operator <(Rational a, Rational b) => a.CompareTo(b) < 0;
    public static bool operator >(Rational a, Rational b) => a.CompareTo(b) > 0;
    public static bool operator <=(Rational a, Rational b) => a.CompareTo(b) <= 0;
    public static bool operator >=(Rational a, Rational b) => a.CompareTo(b) >= 0;

    public bool Equals(Rational other) => _num == other._num && Denominator == other.Denominator;
    public override bool Equals(object? obj) => obj is Rational r && Equals(r);
    public override int GetHashCode() => HashCode.Combine(_num, Denominator);
    public int CompareTo(Rational other) => (_num * other.Denominator).CompareTo(other._num * Denominator);

    public static Rational Min(Rational a, Rational b) => a <= b ? a : b;
    public static Rational Max(Rational a, Rational b) => a >= b ? a : b;

    /// <summary>10^scale as a rational (scale may be negative).</summary>
    public static Rational Pow10(int scale) => scale >= 0 ? FromInteger(BigInteger.Pow(10, scale)) : Create(BigInteger.One, BigInteger.Pow(10, -scale));

    /// <summary>Rounds to <paramref name="scale"/> decimal places with the given mode: the single rounding step of a calculation.</summary>
    public Rational Round(int scale, RoundingMode mode)
    {
        var f = Pow10(scale);
        var x = this * f;
        var q = BigInteger.DivRem(x._num, x.Denominator, out var rem); // truncates toward zero
        if (!rem.IsZero)
        {
            int sign = x._num.Sign;
            int half = (BigInteger.Abs(rem) * 2).CompareTo(x.Denominator); // <0 below half, 0 exactly half, >0 above
            bool away = mode switch
            {
                RoundingMode.Down => false,
                RoundingMode.Up => true,
                RoundingMode.Floor => sign < 0,
                RoundingMode.Ceiling => sign > 0,
                RoundingMode.HalfUp => half >= 0,
                RoundingMode.HalfEven => half > 0 || (half == 0 && !q.IsEven),
                _ => throw new ArgumentOutOfRangeException(nameof(mode)),
            };
            if (away) q += sign;
        }
        return Create(q, f.Numerator);
    }

    /// <summary>True when the value has a finite decimal expansion of at most <paramref name="scale"/> places.</summary>
    public bool HasScaleAtMost(int scale) => (this * Pow10(scale)).IsInteger;

    /// <summary>Fixed-point text with exactly <paramref name="scale"/> places; the value must already be at that scale.</summary>
    public string ToFixedString(int scale)
    {
        if (!HasScaleAtMost(scale)) throw new InvalidOperationException($"{ToCanonicalString()} needs rounding to {scale} places first");
        var units = (this * Pow10(scale)).Numerator;
        var neg = units.Sign < 0;
        var digits = BigInteger.Abs(units).ToString(CultureInfo.InvariantCulture).PadLeft(scale + 1, '0');
        var sb = new StringBuilder();
        if (neg) sb.Append('-');
        sb.Append(digits, 0, digits.Length - scale);
        if (scale > 0) sb.Append('.').Append(digits, digits.Length - scale, scale);
        return sb.ToString();
    }

    /// <summary>The canonical text: "n" for integers, otherwise "n/d" reduced with a positive denominator.</summary>
    public string ToCanonicalString() => IsInteger
        ? _num.ToString(CultureInfo.InvariantCulture)
        : _num.ToString(CultureInfo.InvariantCulture) + "/" + Denominator.ToString(CultureInfo.InvariantCulture);

    public override string ToString() => ToCanonicalString();

    /// <summary>Parses untrusted input in the strict grammar of <see cref="StrictGrammar"/> (at most 400 characters).</summary>
    public static Rational Parse(string text) => StrictGrammar.ParseRational(text);

    /// <summary>
    /// The longest canonical text a value within the default precision budget can have: sign, two numbers of
    /// at most ⌊65536·log10 2⌋ + 1 = 19,729 digits, and the fraction bar. Stored canonical values never exceed it.
    /// </summary>
    public const int MaxCanonicalLength = 1 + 19_729 + 1 + 19_729;

    /// <summary>
    /// Parses stored canonical text ("n" or "n/d", reduced, positive denominator, not "n/1"): the inverse of
    /// <see cref="ToCanonicalString"/> for every value the kernel can produce. Anything else is refused.
    /// </summary>
    public static Rational ParseCanonical(string text)
    {
        if (text is null || text.Length == 0 || text.Length > MaxCanonicalLength) throw new FormatException($"canonical text is 1 to {MaxCanonicalLength} characters");
        int slash = text.IndexOf('/');
        var n = StrictGrammar.ParseInteger(slash < 0 ? text : text[..slash]);
        var d = slash < 0 ? BigInteger.One : StrictGrammar.ParseInteger(text[(slash + 1)..], allowSign: false);
        var r = Create(n, d);
        if (r.ToCanonicalString() != text) throw new FormatException("not canonical: \"" + (text.Length > 40 ? text[..40] + "…" : text) + "\"");
        return r;
    }
}

/// <summary>How the one rounding step of a calculation resolves the discarded part.</summary>
public enum RoundingMode
{
    /// <summary>Ties to the even neighbour (banker's rounding).</summary>
    HalfEven,
    /// <summary>Ties away from zero.</summary>
    HalfUp,
    /// <summary>Toward zero (truncation).</summary>
    Down,
    /// <summary>Away from zero.</summary>
    Up,
    /// <summary>Toward negative infinity.</summary>
    Floor,
    /// <summary>Toward positive infinity.</summary>
    Ceiling,
}
