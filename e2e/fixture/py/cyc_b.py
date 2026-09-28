"""Cycle half B: imports cyc_a, which imports back. Static indexer only, never executed."""

from py import cyc_a


def pong():
    return cyc_a.ping()
