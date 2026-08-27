# Agent Context Infrastructure

## Vision

Agent Context Infrastructure is the layer that makes context durable, accessible, and manageable
across agents. Here, context is the umbrella abstraction for long-running AI agents: the state
needed to continue an execution, the workspace where agents do their work, durable memory carried
across sessions, knowledge synthesized from that memory, and artifacts produced along the way.

In short:

> **Context = Execution + Workspace + Memory + Knowledge + Artifacts**

Context Service is the component in this repository that provides this infrastructure without
requiring every agent system to build its own storage and lifecycle mechanisms.

## Layers of context

**Execution context** is the active state of an agent: harness state, prompts, tool history, and conversations. It lets an interrupted or restarted agent continue its work.

**Workspace context** is where work happens: Git repositories, sandbox filesystems, source files, generated artifacts, and other execution assets.

**Memory** is durable experience carried beyond one execution or session. It may include observations, decisions, Markdown documents, reports, PDFs, and other structured artifacts.

**Knowledge** is the understanding distilled from memory into reusable forms such as living wikis, documentation, reports, and knowledge bases.

These layers are related but distinct. A workspace is not automatically memory, and a collection of memories is not automatically knowledge.

## Memory as infrastructure

Agent memory is often treated as a capability embedded separately in each agent system. The broader opportunity is to treat memory—and eventually context—as shared infrastructure.

A Context Service could provide common capabilities for storing, organizing, retrieving, sharing, governing, synchronizing, and evolving context across long-running and collaborative agent systems.

The library is a useful analogy:

- memories are the books;
- the Context Service is the library system that manages cataloging, access, provenance, versions, and lifecycle;
- the knowledge layer continuously studies the collection and produces higher-level understanding.

Memory captures **what happened**. Knowledge captures **what has been learned**.

## Opportunity

This model creates opportunities for:

- shared memory and workspaces across agents;
- promotion of useful information from transient execution into durable memory;
- collaborative memory management with clear ownership and provenance;
- living knowledge bases that agents continuously refine;
- context that survives individual agents, sessions, and compute instances;
- a common context layer usable by different agent runtimes and platforms.

## How this repository begins

The vision is intentionally broader than the first implementation.

This repository begins with one concrete problem: dynamically creating sandbox compute with durable workspace storage for serverless-harness. That small slice provides a place to learn what the service must own, which abstractions are real, and where the boundaries belong.

Broader memory and knowledge capabilities will be added only as working use cases make them clear.
