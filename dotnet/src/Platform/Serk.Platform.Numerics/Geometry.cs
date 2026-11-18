namespace Serk.Platform.Numerics;

/// <summary>A point with exact rational coordinates.</summary>
public readonly record struct Point2(Rational X, Rational Y)
{
    /// <summary>The exact value of floating-point model coordinates (no decimal rounding).</summary>
    public static Point2 FromDoubles(double x, double y) => new(Rational.FromDoubleExact(x), Rational.FromDoubleExact(y));
}

/// <summary>
/// Robust plane geometry for take-off: predicates evaluated exactly, so the answer is never a rounding
/// accident, and tolerances that are explicit values rather than hidden epsilons.
/// </summary>
public static class Geometry
{
    /// <summary>Sign of the turn a→b→c: +1 counter-clockwise, −1 clockwise, 0 collinear. Exact.</summary>
    public static int Orientation(Point2 a, Point2 b, Point2 c) => ((b.X - a.X) * (c.Y - a.Y) - (b.Y - a.Y) * (c.X - a.X)).Sign;

    /// <summary>The exact signed area of a simple polygon (shoelace); positive when counter-clockwise.</summary>
    public static Rational SignedArea(IReadOnlyList<Point2> ring)
    {
        if (ring.Count < 3) throw new ArgumentException("a polygon has at least three vertices", nameof(ring));
        var twice = Rational.Zero;
        for (int i = 0; i < ring.Count; i++)
        {
            var p = ring[i];
            var q = ring[(i + 1) % ring.Count];
            twice += p.X * q.Y - q.X * p.Y;
        }
        return twice / 2;
    }

    /// <summary>True when two segments share a point (touching counts), decided exactly.</summary>
    public static bool SegmentsIntersect(Point2 p1, Point2 p2, Point2 q1, Point2 q2)
    {
        int o1 = Orientation(p1, p2, q1), o2 = Orientation(p1, p2, q2), o3 = Orientation(q1, q2, p1), o4 = Orientation(q1, q2, p2);
        if (o1 != o2 && o3 != o4) return true;
        static bool On(Point2 a, Point2 b, Point2 c) =>
            c.X >= Rational.Min(a.X, b.X) && c.X <= Rational.Max(a.X, b.X) && c.Y >= Rational.Min(a.Y, b.Y) && c.Y <= Rational.Max(a.Y, b.Y);
        return (o1 == 0 && On(p1, p2, q1)) || (o2 == 0 && On(p1, p2, q2)) || (o3 == 0 && On(q1, q2, p1)) || (o4 == 0 && On(q1, q2, p2));
    }

    /// <summary>True when no two non-adjacent edges meet: only such polygons have a meaningful area.</summary>
    public static bool IsSimple(IReadOnlyList<Point2> ring)
    {
        int n = ring.Count;
        for (int i = 0; i < n; i++)
            for (int j = i + 1; j < n; j++)
            {
                if (j == i + 1 || (i == 0 && j == n - 1)) continue;
                if (SegmentsIntersect(ring[i], ring[(i + 1) % n], ring[j], ring[(j + 1) % n])) return false;
            }
        return true;
    }
}

/// <summary>An explicit absolute tolerance in a unit (never a hidden epsilon).</summary>
public sealed record Tolerance(Rational Absolute, Unit Unit)
{
    public bool Within(Quantity a, Quantity b) => (a - b).SiValue.Abs() <= Absolute * Unit.ToSi + a.ErrorBound + b.ErrorBound;
}
