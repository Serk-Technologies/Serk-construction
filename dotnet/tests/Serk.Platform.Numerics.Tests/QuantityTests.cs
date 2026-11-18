using FsCheck;
using FsCheck.Fluent;
using FsCheck.Xunit;
using Serk.Platform.Numerics;

namespace Serk.Platform.Numerics.Tests;

public class QuantityTests
{
    [Fact]
    public void Units_convert_exactly()
    {
        var slab = Quantity.Exact(250, Unit.Millimetre) * Quantity.Exact(12, Unit.Metre) * Quantity.Exact(Rational.Parse("7.5"), Unit.Metre);
        Assert.Equal(Dimension.Volume, slab.Dimension);
        Assert.Equal(Rational.Parse("22.5"), slab.In(Unit.CubicMetre));
        Assert.Equal(22500, slab.In(Unit.Litre));
        Assert.Equal("45/2 [L3]", slab.ToCanonicalString());
    }

    [Fact]
    public void Dimensions_never_mix()
    {
        var length = Quantity.Exact(3, Unit.Metre);
        var mass = Quantity.Exact(3, Unit.Kilogram);
        Assert.Throws<DimensionMismatchException>(() => length + mass);
        Assert.Throws<DimensionMismatchException>(() => length.In(Unit.Kilogram));
        var area = length * length;
        Assert.Throws<DimensionMismatchException>(() => area - length);
    }

    [Fact]
    public void Pressure_from_force_over_area()
    {
        var p = Quantity.Exact(450, Unit.Kilonewton) / (Quantity.Exact(300, Unit.Millimetre) * Quantity.Exact(300, Unit.Millimetre));
        Assert.Equal(Dimension.Pressure, p.Dimension);
        Assert.Equal(5, p.In(Unit.Megapascal));
    }

    [Fact]
    public void Approximate_quantities_carry_and_propagate_their_error_bound()
    {
        var modelArea = Quantity.Approximate(Rational.Parse("100.00"), Rational.Parse("0.05"), Unit.SquareMetre);
        var thickness = Quantity.Exact(Rational.Parse("0.2"), Unit.Metre);
        var volume = modelArea * thickness;
        Assert.False(volume.IsExact);
        Assert.Equal(Rational.Parse("0.01"), volume.ErrorBound); // 0.2 × 0.05
        Assert.Throws<InexactQuantityException>(() => volume.RequireExact("BOQ quantity"));
        var exact = Quantity.Exact(20, Unit.CubicMetre);
        Assert.Equal(exact, exact.RequireExact("BOQ quantity"));
    }

    [Fact]
    public void Tolerances_are_explicit()
    {
        var tol = new Tolerance(Rational.Parse("0.5"), Unit.Millimetre);
        Assert.True(tol.Within(Quantity.Exact(Rational.Parse("1000.4"), Unit.Millimetre), Quantity.Exact(1, Unit.Metre)));
        Assert.False(tol.Within(Quantity.Exact(Rational.Parse("1000.6"), Unit.Millimetre), Quantity.Exact(1, Unit.Metre)));
    }

    [Property]
    public Property The_error_bound_contains_every_true_value() => Prop.ForAll(
        (from a in Gen.Choose(-1000, 1000)
         from ea in Gen.Choose(1, 50)
         from b in Gen.Choose(-1000, 1000)
         from eb in Gen.Choose(1, 50)
         from ta in Gen.Choose(-100, 100)
         from tb in Gen.Choose(-100, 100)
         select (a, ea, b, eb, ta, tb)).ToArbitrary(),
        t =>
        {
            // Pick true values inside each bound; the sum and product must stay inside the propagated bound.
            var (a, ea, b, eb, ta, tb) = t;
            var qa = Quantity.Approximate(a, ea, Unit.Metre);
            var qb = Quantity.Approximate(b, eb, Unit.Metre);
            var trueA = Rational.FromInteger(a) + Rational.Create(ta, 100) * ea;
            var trueB = Rational.FromInteger(b) + Rational.Create(tb, 100) * eb;
            var sum = qa + qb;
            var product = qa * qb;
            return (trueA + trueB - sum.SiValue).Abs() <= sum.ErrorBound && (trueA * trueB - product.SiValue).Abs() <= product.ErrorBound;
        });
}
