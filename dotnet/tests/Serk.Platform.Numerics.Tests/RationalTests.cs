using System.Numerics;
using FsCheck;
using FsCheck.Fluent;
using FsCheck.Xunit;
using Serk.Platform.Numerics;

namespace Serk.Platform.Numerics.Tests;

public class RationalTests
{
    [Fact]
    public void One_third_stays_exact_where_decimal_would_not()
    {
        var third = Rational.Create(1, 3);
        Assert.Equal(Rational.One, third + third + third);
        // System.Decimal loses it: this is why the kernel does not use decimal for exact work.
        Assert.NotEqual(1m, 1m / 3m * 3m);
    }

    [Theory]
    [InlineData("0", "0")]
    [InlineData("12", "12")]
    [InlineData("-12.50", "-25/2")]
    [InlineData("0.125", "1/8")]
    [InlineData("6/4", "3/2")]
    [InlineData("-6/4", "-3/2")]
    [InlineData("123456789012345678901234567890.000000000000000000001", "123456789012345678901234567890000000000000000000001/1000000000000000000000")]
    public void Strict_grammar_accepts_one_spelling_and_reduces(string text, string canonical) =>
        Assert.Equal(canonical, Rational.Parse(text).ToCanonicalString());

    [Theory]
    [InlineData("")]
    [InlineData(" 1")]
    [InlineData("1 ")]
    [InlineData("+1")]
    [InlineData("01")]
    [InlineData("-0")]
    [InlineData("-0.00")]
    [InlineData("1e3")]
    [InlineData("1E3")]
    [InlineData(".5")]
    [InlineData("5.")]
    [InlineData("1,000")]
    [InlineData("1_000")]
    [InlineData("NaN")]
    [InlineData("Infinity")]
    [InlineData("0x10")]
    [InlineData("1/0")]
    [InlineData("1/-2")]
    [InlineData("1/02")]
    [InlineData("1.5/2")]
    [InlineData("١٢")] // Arabic-Indic digits: not ASCII digits
    public void Strict_grammar_refuses_everything_else(string text) => Assert.Throws<FormatException>(() => Rational.Parse(text));

    [Fact]
    public void Overlong_input_is_refused_before_parsing() =>
        Assert.Throws<FormatException>(() => Rational.Parse(new string('9', StrictGrammar.MaxLength + 1)));

    [Theory]
    [InlineData("2.5", RoundingMode.HalfEven, "2")]
    [InlineData("3.5", RoundingMode.HalfEven, "4")]
    [InlineData("-2.5", RoundingMode.HalfEven, "-2")]
    [InlineData("2.5", RoundingMode.HalfUp, "3")]
    [InlineData("-2.5", RoundingMode.HalfUp, "-3")]
    [InlineData("2.9", RoundingMode.Down, "2")]
    [InlineData("-2.9", RoundingMode.Down, "-2")]
    [InlineData("2.1", RoundingMode.Up, "3")]
    [InlineData("-2.1", RoundingMode.Up, "-3")]
    [InlineData("-2.1", RoundingMode.Floor, "-3")]
    [InlineData("-2.9", RoundingMode.Ceiling, "-2")]
    [InlineData("2.4999999999999999999999999", RoundingMode.HalfUp, "2")]
    public void Rounding_modes_to_integers(string value, RoundingMode mode, string expected) =>
        Assert.Equal(Rational.Parse(expected), Rational.Parse(value).Round(0, mode));

    [Fact]
    public void Exact_double_conversion_shows_the_binary_value() =>
        Assert.Equal("3602879701896397/36028797018963968", Rational.FromDoubleExact(0.1).ToCanonicalString());

    [Fact]
    public void Budget_stops_unbounded_growth()
    {
        using (ComputeBudget.Begin(maxBits: 256, maxOperations: 1_000_000))
        {
            var x = Rational.Create(1, 3);
            Assert.Throws<ComputeBudgetExceededException>(() => { for (int i = 0; i < 64; i++) x *= x; });
        }
        using (ComputeBudget.Begin(maxBits: 4096, maxOperations: 10))
        {
            var y = Rational.One;
            Assert.Throws<ComputeBudgetExceededException>(() => { for (int i = 0; i < 20; i++) y += Rational.One; });
        }
        Assert.Null(ComputeBudget.Current);
    }

    private static Arbitrary<Rational> Rationals() =>
        (from n in ArbMap.Default.GeneratorFor<long>()
         from d in ArbMap.Default.GeneratorFor<long>().Where(d => d != 0)
         select Rational.Create(n, d)).ToArbitrary();

    [Property]
    public Property Field_laws_hold_exactly() => Prop.ForAll(Rationals(), Rationals(), Rationals(), (a, b, c) =>
        (a + b) + c == a + (b + c) && a * (b + c) == a * b + a * c && a + b == b + a && a - a == Rational.Zero
        && (b.IsZero || (a / b) * b == a));

    [Property]
    public Property Canonical_text_round_trips() => Prop.ForAll(Rationals(), a => Rational.ParseCanonical(a.ToCanonicalString()) == a);

    [Fact]
    public void Canonical_text_round_trips_at_the_precision_budget()
    {
        // The largest numerator and denominator the default budget allows (65,536 bits each), coprime.
        var n = BigInteger.Pow(2, 65_536) - 1;
        var d = BigInteger.Pow(2, 65_535) - 1;
        var big = Rational.Create(-n, d);
        var text = big.ToCanonicalString();
        Assert.True(text.Length <= Rational.MaxCanonicalLength, $"{text.Length} > {Rational.MaxCanonicalLength}");
        Assert.Equal(big, Rational.ParseCanonical(text));
        Assert.Throws<ComputeBudgetExceededException>(() => Rational.Create(BigInteger.Pow(2, 65_536), 1));
    }

    [Theory]
    [InlineData("2/4")]
    [InlineData("3/1")]
    [InlineData("1.5")]
    [InlineData("-0")]
    [InlineData("1/-2")]
    public void Stored_text_must_be_canonical(string text) => Assert.Throws<FormatException>(() => Rational.ParseCanonical(text));

    [Property]
    public Property Rounding_moves_by_less_than_one_unit_and_is_idempotent() => Prop.ForAll(Rationals(), a =>
        Enum.GetValues<RoundingMode>().All(m =>
        {
            var r = a.Round(2, m);
            return (r - a).Abs() < Rational.Create(1, 100) && r.Round(2, m) == r && r.HasScaleAtMost(2);
        }));

    [Property]
    public Property Half_even_never_differs_from_exact_by_more_than_half_a_unit() => Prop.ForAll(Rationals(), a =>
        (a.Round(2, RoundingMode.HalfEven) - a).Abs() <= Rational.Create(1, 200));

    [Property]
    public Property Doubles_convert_exactly() => Prop.ForAll(ArbMap.Default.ArbFor<NormalFloat>(), f =>
    {
        var d = f.Get;
        var r = Rational.FromDoubleExact(d);
        return (double)r.Numerator / (double)r.Denominator == d || BigInteger.Abs(r.Numerator).GetBitLength() > 1000;
    });
}
