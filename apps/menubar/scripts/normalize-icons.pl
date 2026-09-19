#!/usr/bin/perl -i -p
# Normalises SVG icons for NSImage (assets/icons/LICENSES.md explains the sources).
# Usage: perl apps/menubar/scripts/normalize-icons.pl assets/icons/*.svg   (edits in place; safe to rerun)
#  1. <svg>: drop role/style/width/height, then set width="24" height="24" (lobehub files say 1em, which NSImage reads as 1 pt).
#  2. Path data: expand packed arc flags ("a6 6 0 013 4" -> "a6 6 0 0 1 3 4"); CoreSVG misreads the packed form.
use strict;
use warnings;

s{<svg\b([^>]*)>}{
    my $attrs = $1;
    $attrs =~ s/\s(?:role|style|width|height)="[^"]*"//g;
    "<svg width=\"24\" height=\"24\"$attrs>"
}ge;
s{(\sd=")([^"]*)}{$1 . fix($2)}ge;

sub fix {
    my $d = shift;
    $d =~ s{([Aa])([^A-Za-z]*)}{$1 . arcs($2)}ge;
    return $d;
}

sub arcs {
    my $s = shift;
    my @out;
    my $num = qr/[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?/;
    while ($s =~ /\S/) {
        for my $k (0 .. 6) {
            $s =~ s/^[\s,]*//;
            if ($k == 3 || $k == 4) {
                $s =~ s/^([01])// or die "bad arc flag near: $s\n";
                push @out, $1;
            } else {
                $s =~ s/^($num)// or die "bad arc number near: $s\n";
                push @out, $1;
            }
        }
        $s =~ s/^[\s,]*//;
    }
    return join(' ', @out);
}
