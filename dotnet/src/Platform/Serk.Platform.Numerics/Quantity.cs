namespace Serk.Platform.Numerics;

/// <summary>
/// An immutable physical quantity: an exact SI value and its dimension, plus how it was obtained.
/// <para>An <b>exact</b> quantity comes from measurement records, drawings dimensions or arithmetic on exact
/// quantities. An <b>approximate</b> quantity comes from geometry (tessellation, floating-point model
/// coordinates) and carries a rigorous absolute error bound that propagates through arithmetic. Authoritative
/// quantity workflows (BOQ, valuation, certification) accept only exact quantities
/// (<see cref="RequireExact"/>).</para>
/// Adding or subtracting quantities of different dimensions is an error, never a coercion.
/// </summary>
public readonly record struct Quantity
{
    public Rational SiValue { get; }
    public Dimension Dimension { get; }
    /// <summary>Zero for exact quantities; otherwise an upper bound on |true value − SiValue|, in SI units.</summary>
    public Rational ErrorBound { get; }
    public bool IsExact => ErrorBound.IsZero;

    private Quantity(Rational si, Dimension d, Rational errorBound)
    {
        if (errorBound.Sign < 0) throw new ArgumentOutOfRangeException(nameof(errorBound), "an error bound is not negative");
        SiValue = si; Dimension = d; ErrorBound = errorBound;
    }

    public static Quantity Exact(Rational value, Unit unit) => new(value * unit.ToSi, unit.Dimension, Rational.Zero);

    /// <summary>A geometry-derived value: <paramref name="absoluteError"/> is in <paramref name="unit"/>.</summary>
    public static Quantity Approximate(Rational value, Rational absoluteError, Unit unit)
    {
        if (absoluteError.IsZero) throw new ArgumentException("an approximate quantity needs a positive error bound; use Exact", nameof(absoluteError));
        return new(value * unit.ToSi, unit.Dimension, absoluteError.Abs() * unit.ToSi);
    }

    public Rational In(Unit unit)
    {
        if (unit.Dimension != Dimension) throw new DimensionMismatchException(Dimension, unit.Dimension, $"express in {unit.Symbol}");
        return SiValue / unit.ToSi;
    }

    public Quantity RequireExact(string purpose) => IsExact ? this
        : throw new InexactQuantityException($"{purpose} needs an exact quantity; this one is geometry-derived (±{ErrorBound.ToCanonicalString()} SI)");

    public static Quantity operator +(Quantity a, Quantity b)
    {
        if (a.Dimension != b.Dimension) throw new DimensionMismatchException(a.Dimension, b.Dimension, "add");
        return new(a.SiValue + b.SiValue, a.Dimension, a.ErrorBound + b.ErrorBound);
    }

    public static Quantity operator -(Quantity a, Quantity b)
    {
        if (a.Dimension != b.Dimension) throw new DimensionMismatchException(a.Dimension, b.Dimension, "subtract");
        return new(a.SiValue - b.SiValue, a.Dimension, a.ErrorBound + b.ErrorBound);
    }

    // |ab − a'b'| ≤ |a|eb + |b|ea + ea·eb
    public static Quantity operator *(Quantity a, Quantity b) =>
        new(a.SiValue * b.SiValue, a.Dimension * b.Dimension, a.SiValue.Abs() * b.ErrorBound + b.SiValue.Abs() * a.ErrorBound + a.ErrorBound * b.ErrorBound);

    public static Quantity operator *(Quantity a, Rational k) => new(a.SiValue * k, a.Dimension, a.ErrorBound * k.Abs());

    public static Quantity operator /(Quantity a, Quantity b)
    {
        if (b.SiValue.IsZero) throw new DivideByZeroException("division by a zero quantity");
        if (!b.IsExact)
        {
            // |a/b − a'/b'| ≤ (|a|eb + |b|ea) / (|b|(|b| − eb)), valid while eb < |b|.
            var bAbs = b.SiValue.Abs();
            if (b.ErrorBound >= bAbs) throw new InexactQuantityException("the divisor's error bound reaches zero: the quotient is unbounded");
            var bound = (a.SiValue.Abs() * b.ErrorBound + bAbs * a.ErrorBound) / (bAbs * (bAbs - b.ErrorBound));
            return new(a.SiValue / b.SiValue, a.Dimension / b.Dimension, bound);
        }
        return new(a.SiValue / b.SiValue, a.Dimension / b.Dimension, a.ErrorBound / b.SiValue.Abs());
    }

    public string ToCanonicalString() => IsExact
        ? $"{SiValue.ToCanonicalString()} [{Dimension.ToCanonicalString()}]"
        : $"{SiValue.ToCanonicalString()} ±{ErrorBound.ToCanonicalString()} [{Dimension.ToCanonicalString()}]";

    public override string ToString() => ToCanonicalString();
}

public sealed class DimensionMismatchException(Dimension a, Dimension b, string operation)
    : Exception($"cannot {operation}: dimension {a.ToCanonicalString()} vs {b.ToCanonicalString()}");

public sealed class InexactQuantityException(string message) : Exception(message);
