using System.Numerics;
using FsCheck;
using FsCheck.Fluent;
using FsCheck.Xunit;
using Serk.Platform.Numerics;
using Serk.Platform.Primitives;

namespace Serk.Platform.Numerics.Tests;

public class MoneyTests
{
    private static readonly Currency Etb = Currency.Of("ETB");

    [Fact]
    public void The_Bible_rate_build_up_reproduces_exactly()
    {
        // EST-06 (Go: TestRateBuildUp...): 7,400 direct cost, +10% overhead, then 15% margin on price.
        var direct = Money.Parse("7400.00", Etb);
        var withOverhead = direct.Multiply(Rational.Parse("1.10"), RoundingMode.HalfEven);
        var price = withOverhead.Divide(Rational.Parse("0.85"), RoundingMode.HalfEven);
        Assert.Equal("8140.00", withOverhead.AmountText);
        Assert.Equal("9576.47", price.AmountText);
    }

    [Fact]
    public void Currencies_never_mix() =>
        Assert.Throws<CurrencyMismatchException>(() => Money.Parse("1", Etb) + Money.Parse("1", Currency.Of("USD")));

    [Theory]
    [InlineData("1.234")]
    [InlineData("01.00")]
    [InlineData("-0.00")]
    [InlineData("1e2")]
    [InlineData("1234567890123456789")]
    public void Strict_amounts(string text) => Assert.Throws<FormatException>(() => Money.Parse(text, Etb));

    [Fact]
    public void Allocation_beyond_64_bits_loses_nothing()
    {
        var huge = Money.Parse("999999999999999999.99", Etb);
        var parts = huge.Allocate([BigInteger.One, BigInteger.One, BigInteger.One]);
        Assert.Equal(huge.Amount, parts.Aggregate(Money.Zero(Etb), (a, b) => a + b).Amount);
        Assert.Equal(["333333333333333333.33", "333333333333333333.33", "333333333333333333.33"], parts.Select(p => p.AmountText));
    }

    [Property]
    public Property Allocation_conserves_every_minor_unit() => Prop.ForAll(
        Gen.Choose(-1_000_000_000, 1_000_000_000).ToArbitrary(), Gen.ListOf(Gen.Choose(0, 1000)).Where(l => l.Count > 0 && l.Sum() > 0).ToArbitrary(),
        (minor, ratios) =>
        {
            var m = Money.FromMinor(minor, Etb);
            var parts = m.Allocate(ratios.Select(r => new BigInteger(r)).ToArray());
            var total = parts.Aggregate(Money.Zero(Etb), (a, b) => a + b);
            // Conservation, and each part within one minor unit of its exact share.
            return total.Amount == m.Amount && parts.Select((p, i) => (p.Amount - m.Amount * Rational.Create(ratios[i], ratios.Sum())).Abs() < Rational.Create(1, 100)).All(x => x);
        });
}

public class GeometryTests
{
    [Fact]
    public void Orientation_is_exact_where_floating_point_fails()
    {
        // Three nearly collinear points from floating-point model coordinates.
        var a = Point2.FromDoubles(0.5, 0.5);
        var b = Point2.FromDoubles(12, 12);
        var c = Point2.FromDoubles(24, 24.000000000000004);
        Assert.Equal(1, Geometry.Orientation(a, b, c));
        Assert.Equal(0, Geometry.Orientation(a, b, Point2.FromDoubles(24, 24)));
    }

    [Fact]
    public void Take_off_area_of_an_L_shaped_slab()
    {
        Point2 P(string x, string y) => new(Rational.Parse(x), Rational.Parse(y));
        var ring = new[] { P("0", "0"), P("10", "0"), P("10", "4"), P("4", "4"), P("4", "7.5"), P("0", "7.5") };
        Assert.True(Geometry.IsSimple(ring));
        Assert.Equal(Rational.Parse("54"), Geometry.SignedArea(ring));
        Assert.Equal(Rational.Parse("-54"), Geometry.SignedArea(ring.Reverse().ToArray()));
        var bowtie = new[] { P("0", "0"), P("2", "2"), P("2", "0"), P("0", "2") };
        Assert.False(Geometry.IsSimple(bowtie));
    }
}

public class CanonicalAndProvenanceTests
{
    [Fact]
    public void Canonical_form_is_key_ordered_and_float_free()
    {
        var s = Canonical.Serialize(new Dictionary<string, object?> { ["b"] = Rational.Create(2, 4), ["a"] = new object?[] { 1, "x\"y", null, true } });
        Assert.Equal("{\"a\":[1,\"x\\\"y\",null,true],\"b\":\"1/2\"}", s);
        Assert.Throws<ArgumentException>(() => Canonical.Serialize(new Dictionary<string, object?> { ["x"] = 0.1 }));
        Assert.Throws<ArgumentException>(() => Canonical.Serialize(new Dictionary<string, object?> { ["x"] = 0.1m }));
    }

    [Fact]
    public void A_recorded_calculation_reproduces_byte_for_byte()
    {
        var policy = RoundingPolicy.MinorUnitHalfEven(2);
        var inputs = new Dictionary<string, object?> { ["direct"] = Rational.Parse("7400"), ["overhead"] = Rational.Parse("0.10"), ["margin"] = Rational.Parse("0.15") };
        CalculationRecord Run() => CalculationRecord.Run("est.rate-build-up", inputs, [policy], () =>
            policy.Apply((Rational)inputs["direct"]! * (1 + (Rational)inputs["overhead"]!) / (1 - (Rational)inputs["margin"]!))).Record;
        var a = Run();
        var b = Run();
        Assert.Equal(a, b with { Policies = a.Policies });
        Assert.Equal(a.Sha256, b.Sha256);
        Assert.Equal("\"957647/100\"", a.ResultCanonical);
        Assert.Equal(["minor-unit-half-even@1"], a.Policies);
        var changed = CalculationRecord.Of("est.rate-build-up", new Dictionary<string, object?>(inputs) { ["margin"] = Rational.Parse("0.16") }, [policy], Rational.Parse("9690.48"));
        Assert.NotEqual(a.Sha256, changed.Sha256);
    }
}

public class EthiopianDateTests
{
    [Theory]
    [InlineData(2026, 9, 11, "2019-01-01")] // Enkutatash after an Ethiopian leap year (2018 % 4 == 2; 2019 follows 2018)
    [InlineData(2023, 9, 12, "2016-01-01")] // year after a leap year (2015 % 4 == 3)
    [InlineData(2026, 11, 16, "2019-03-07")]
    public void Known_dates(int gy, int gm, int gd, string iso)
    {
        var e = EthiopianDate.FromGregorian(new DateOnly(gy, gm, gd));
        Assert.Equal(iso, e.Iso);
        Assert.Equal(new DateOnly(gy, gm, gd), e.ToGregorian());
    }

    [Theory]
    [InlineData("2015-13-06")] // leap Pagume has 6 days
    public void Leap_Pagume(string iso) => Assert.Equal(6, EthiopianDate.Parse(iso).Day);

    [Theory]
    [InlineData("2016-13-06")]
    [InlineData("2016-14-01")]
    [InlineData("2016-1-01")]
    [InlineData("+016-01-01")]
    [InlineData("2016-01-31")]
    public void Refused(string text) => Assert.Throws<FormatException>(() => EthiopianDate.Parse(text));

    [Property]
    public Property Round_trips_through_the_Gregorian_calendar() => Prop.ForAll(Gen.Choose(-200_000, 400_000).ToArbitrary(), offset =>
    {
        var g = new DateOnly(1970, 1, 1).AddDays(offset);
        var e = EthiopianDate.FromGregorian(g);
        return !e.IsValid || e.ToGregorian() == g;
    });
}
