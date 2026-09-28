"""Cycle half A: imports cyc_b, which imports back. Static indexer only, never executed."""

from py import cyc_b


def ping():
    return cyc_b.pong()
