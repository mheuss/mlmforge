//! Streamline engine — manages all streams for one streamline structure.

use crate::snapshot::SnapshotConsistencyError;
use std::collections::{HashMap, HashSet};

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::config::streamline::StreamAssignmentMode;
use crate::tree::unilevel::UnilevelTree;

use super::error::StreamlineError;
use super::types::{
    AddMemberResult, ExpansionResult, FreezeResult, MemberInfo, RemoveMemberResult, StreamPosition,
    StreamSummary,
};

/// A single stream in the streamline structure.
#[derive(Debug, Serialize, Deserialize)]
pub struct Stream {
    /// Stream ID (1-based, sequential).
    pub id: u32,
    /// The width-1 UnilevelTree for this stream.
    pub tree: UnilevelTree,
    /// The user who owns this stream (earned via rank).
    pub owner_id: Uuid,
    /// Whether this stream is frozen due to rank demotion.
    pub frozen: bool,
    /// When this stream was created.
    pub created_at: i64,
    /// The user at the bottom of the chain (for O(1) append).
    /// None when the stream is empty.
    bottom: Option<Uuid>,
}

/// Engine configuration for stream assignment and lifecycle.
///
/// Separate from StreamlineCommissionConfig. This controls placement
/// and stream management, not commission calculation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamlineConfig {
    /// How new enrollees are assigned to streams.
    pub assignment_mode: StreamAssignmentMode,
    /// Whether sponsors can choose a stream for each enrollment.
    pub enrollment_stream_choice: bool,
    /// Whether excess streams freeze on demotion (vs destroyed).
    pub freeze_on_demotion: bool,
}

/// Manages all streams for a single streamline structure.
///
/// Tracks stream membership, ownership, and lifecycle.
/// Does NOT implement TreeNavigator because streamline's
/// one-user-to-many-positions model violates the single-position
/// assumption. Individual streams are UnilevelTrees that do
/// implement TreeNavigator.
#[derive(Debug, Serialize, Deserialize)]
pub struct StreamlineEngine {
    /// All streams keyed by stream ID.
    streams: HashMap<u32, Stream>,

    /// Maps each user to all streams they have a position on.
    user_streams: HashMap<Uuid, Vec<u32>>,

    /// Maps each user to the streams they own.
    stream_owners: HashMap<Uuid, Vec<u32>>,

    /// Next stream ID to allocate.
    next_stream_id: u32,

    /// Engine configuration.
    config: StreamlineConfig,
}

impl StreamlineEngine {
    /// Prove a restored engine's indexes agree with the streams they index.
    pub fn validate_restored(&self) -> Result<(), SnapshotConsistencyError> {
        let mut nested: Option<(u32, SnapshotConsistencyError)> = None;
        for (key, stream) in &self.streams {
            // A mismatch between the map key and the stream's own id makes a
            // lookup by the other value silently miss.
            let found = if *key != stream.id {
                Some(SnapshotConsistencyError::StreamIdMismatch {
                    key: *key,
                    stream_id: stream.id,
                })
            } else if let Err(source) = stream.tree.validate_restored() {
                Some(SnapshotConsistencyError::Stream {
                    stream_id: stream.id,
                    source: Box::new(source),
                })
            } else {
                match stream.bottom {
                    Some(bottom_id) if !stream.tree.contains(bottom_id) => {
                        Some(SnapshotConsistencyError::StreamBottomNotInTree {
                            stream_id: stream.id,
                            user_id: bottom_id,
                        })
                    }
                    None if !stream.tree.user_ids().is_empty() => {
                        let node_count = stream.tree.user_ids().len();
                        Some(SnapshotConsistencyError::StreamBottomMissing {
                            stream_id: stream.id,
                            node_count,
                        })
                    }
                    _ => None,
                }
            };
            if let Some(err) = found {
                if nested.as_ref().is_none_or(|(held, _)| key < held) {
                    nested = Some((*key, err));
                }
            }
        }
        if let Some((_, err)) = nested {
            return Err(err);
        }

        // Runs after the walk above, which is what establishes that a map key
        // is the stream's own id. create_stream inserts on the cursor with no
        // occupancy check, so a cursor at or below a live key replaces that
        // stream and drops every member in its tree.
        if let Some(highest) = self.streams.keys().copied().max() {
            if self.next_stream_id <= highest {
                return Err(SnapshotConsistencyError::StreamIdCursorNotPastEnd {
                    next_stream_id: self.next_stream_id,
                    highest_stream_id: highest,
                });
            }
        }
        // Past every live key is not enough. create_stream assigns the cursor
        // and then increments it, so the last id is one the engine can hand
        // out but not advance beyond.
        if self.next_stream_id == u32::MAX {
            return Err(SnapshotConsistencyError::StreamIdCursorExhausted {
                next_stream_id: self.next_stream_id,
            });
        }

        // The reverse of the user_streams walk below. Held on the (stream,
        // user) pair, not the stream alone, since user_ids() returns hash
        // order.
        //
        // The membership pairs are collected into a set first. A Vec::contains
        // per user costs the length of that user's stream list, which makes the
        // walk quadratic in the streams one user holds. Design NFR 4 is O(N+E).
        let mut indexed_pairs: HashSet<(Uuid, u32)> = HashSet::new();
        for (user_id, ids) in &self.user_streams {
            for id in ids {
                indexed_pairs.insert((*user_id, *id));
            }
        }
        let mut unindexed: Option<(u32, Uuid, SnapshotConsistencyError)> = None;
        for stream in self.streams.values() {
            for user_id in stream.tree.user_ids() {
                let indexed = indexed_pairs.contains(&(user_id, stream.id));
                if !indexed
                    && unindexed.as_ref().is_none_or(|(held_id, held_user, _)| {
                        (stream.id, user_id) < (*held_id, *held_user)
                    })
                {
                    unindexed = Some((
                        stream.id,
                        user_id,
                        SnapshotConsistencyError::TreeUserNotIndexed {
                            stream_id: stream.id,
                            user_id,
                        },
                    ));
                }
            }
        }
        if let Some((_, _, err)) = unindexed {
            return Err(err);
        }

        let mut fault: Option<(Uuid, SnapshotConsistencyError)> = None;
        for (user_id, stream_ids) in &self.user_streams {
            // An entry listing no streams is itself a contradiction:
            // membership means at least one. It also slips past the walk
            // below, whose find_map over an empty slice yields nothing.
            let found = if stream_ids.is_empty() {
                Some(SnapshotConsistencyError::UserStreamsEntryEmpty { user_id: *user_id })
            } else {
                stream_ids
                    .iter()
                    .find_map(|stream_id| match self.streams.get(stream_id) {
                        None => Some(SnapshotConsistencyError::StreamAbsent {
                            user_id: *user_id,
                            stream_id: *stream_id,
                        }),
                        Some(stream) if !stream.tree.contains(*user_id) => {
                            Some(SnapshotConsistencyError::UserNotInStreamTree {
                                user_id: *user_id,
                                stream_id: *stream_id,
                            })
                        }
                        Some(_) => None,
                    })
            };
            if let Some(err) = found {
                if fault.as_ref().is_none_or(|(held, _)| user_id < held) {
                    fault = Some((*user_id, err));
                }
            }
        }
        if let Some((_, err)) = fault {
            return Err(err);
        }

        let mut owner_fault: Option<(Uuid, SnapshotConsistencyError)> = None;
        for (user_id, stream_ids) in &self.stream_owners {
            let found = stream_ids.iter().find_map(|stream_id| {
                // A distinct variant, so the message names the map that
                // actually observed the absence.
                match self.streams.get(stream_id) {
                    None => Some(SnapshotConsistencyError::OwnerStreamAbsent {
                        user_id: *user_id,
                        stream_id: *stream_id,
                    }),
                    Some(stream) if stream.owner_id != *user_id => {
                        Some(SnapshotConsistencyError::StreamOwnerMismatch {
                            user_id: *user_id,
                            stream_id: *stream_id,
                            owner_id: stream.owner_id,
                        })
                    }
                    Some(_) => None,
                }
            });
            if let Some(err) = found {
                if owner_fault.as_ref().is_none_or(|(held, _)| user_id < held) {
                    owner_fault = Some((*user_id, err));
                }
            }
        }
        if let Some((_, err)) = owner_fault {
            return Err(err);
        }

        Ok(())
    }
    /// Creates a new engine with one empty initial stream.
    ///
    /// The initial stream has no owner yet. The first member added
    /// becomes the bootstrap case.
    pub fn new(config: StreamlineConfig, timestamp: i64) -> Self {
        let initial_stream = Stream {
            id: 1,
            tree: UnilevelTree::new(),
            owner_id: Uuid::nil(),
            frozen: false,
            created_at: timestamp,
            bottom: None,
        };
        let mut streams = HashMap::new();
        streams.insert(1, initial_stream);

        Self {
            streams,
            user_streams: HashMap::new(),
            stream_owners: HashMap::new(),
            next_stream_id: 2,
            config,
        }
    }

    /// Returns the number of streams.
    pub fn stream_count(&self) -> usize {
        self.streams.len()
    }

    /// Returns a reference to a stream by ID.
    pub fn get_stream(&self, stream_id: u32) -> Option<&Stream> {
        self.streams.get(&stream_id)
    }

    /// Returns an iterator over active (unfrozen) streams.
    ///
    /// A frozen stream is omitted entirely rather than yielded empty.
    pub fn active_streams(&self) -> impl Iterator<Item = &Stream> {
        self.streams.values().filter(|s| !s.frozen)
    }

    /// Returns true if the user has a position in any stream.
    pub fn contains_member(&self, user_id: Uuid) -> bool {
        self.user_streams.contains_key(&user_id)
    }

    /// Returns the stream IDs a member has positions on.
    pub fn get_member_streams(&self, user_id: Uuid) -> Option<&Vec<u32>> {
        self.user_streams.get(&user_id)
    }

    /// Removes a member from all streams they have positions on.
    ///
    /// In a linear chain, removing a non-leaf node requires chain
    /// compaction: collect descendants, remove from leaf up, re-add
    /// descendants under the removed node's parent. When the removed
    /// user was a descendant's sponsor, the new parent becomes the
    /// sponsor instead.
    pub fn remove_member(
        &mut self,
        user_id: Uuid,
        _timestamp: i64,
    ) -> Result<RemoveMemberResult, StreamlineError> {
        let stream_ids = self
            .user_streams
            .remove(&user_id)
            .ok_or(StreamlineError::MemberNotFound(user_id))?;

        let mut removed_from = Vec::new();

        for &stream_id in &stream_ids {
            let stream = match self.streams.get_mut(&stream_id) {
                Some(s) => s,
                None => continue,
            };

            if !stream.tree.contains(user_id) {
                continue;
            }

            // Collect the chain below the target for compaction.
            let descendants: Vec<(Uuid, Uuid, i64)> = stream
                .tree
                .get_downline(user_id, 0)
                .unwrap_or_default()
                .iter()
                .map(|node| {
                    let sponsor_id = stream
                        .tree
                        .get_sponsor(node.user_id)
                        .ok()
                        .flatten()
                        .map(|s| s.user_id)
                        .unwrap_or(node.user_id);
                    (node.user_id, sponsor_id, node.enrolled_at)
                })
                .collect();

            // Get the parent of the target for re-attachment.
            let parent_id = stream
                .tree
                .get_parent(user_id)
                .ok()
                .flatten()
                .map(|p| p.user_id);

            // Remove from leaf up: descendants in reverse, then the target.
            for &(desc_id, _, _) in descendants.iter().rev() {
                stream.tree.remove_node(desc_id).map_err(|e| {
                    StreamlineError::TreeError(format!("remove descendant {desc_id}: {e}"))
                })?;
                if let Some(streams) = self.user_streams.get_mut(&desc_id) {
                    streams.retain(|&id| id != stream_id);
                    if streams.is_empty() {
                        self.user_streams.remove(&desc_id);
                    }
                }
            }
            stream
                .tree
                .remove_node(user_id)
                .map_err(|e| StreamlineError::TreeError(format!("remove target {user_id}: {e}")))?;

            // Re-add descendants under the removed node's parent.
            // When a descendant's sponsor was the removed user, use the
            // new parent as sponsor instead.
            let mut current_parent = parent_id;
            for (i, &(desc_id, original_sponsor, enrolled_at)) in descendants.iter().enumerate() {
                let sponsor = if original_sponsor == user_id {
                    current_parent.unwrap_or(desc_id)
                } else {
                    original_sponsor
                };

                match current_parent {
                    Some(pid) => {
                        stream
                            .tree
                            .add_node(desc_id, pid, sponsor, enrolled_at)
                            .map_err(|e| {
                                StreamlineError::TreeError(format!(
                                    "re-add descendant {desc_id}: {e}"
                                ))
                            })?;
                    }
                    None => {
                        stream.tree.add_root(desc_id, enrolled_at).map_err(|e| {
                            StreamlineError::TreeError(format!("re-root descendant {desc_id}: {e}"))
                        })?;
                    }
                }
                self.user_streams
                    .entry(desc_id)
                    .or_default()
                    .push(stream_id);
                current_parent = Some(desc_id);

                if i == descendants.len() - 1 {
                    stream.bottom = Some(desc_id);
                }
            }

            if descendants.is_empty() {
                stream.bottom = parent_id;
            }

            if stream.tree.user_ids().is_empty() {
                stream.bottom = None;
            }

            // Transfer ownership if the removed user owned this stream.
            if stream.owner_id == user_id {
                // First remaining member becomes owner, or mark as unowned.
                let new_owner = stream
                    .tree
                    .user_ids()
                    .into_iter()
                    .find(|uid| stream.tree.get_parent(*uid).ok().flatten().is_none());
                match new_owner {
                    Some(nid) => {
                        stream.owner_id = nid;
                        self.stream_owners.entry(nid).or_default().push(stream_id);
                    }
                    None => {
                        stream.owner_id = Uuid::nil();
                    }
                }
            }

            removed_from.push(stream_id);
        }

        // Handle owned streams not visited in the loop above (e.g., empty
        // expanded streams the user never got placed into).
        if let Some(owned_ids) = self.stream_owners.remove(&user_id) {
            for oid in owned_ids {
                if let Some(stream) = self.streams.get_mut(&oid) {
                    if stream.owner_id == user_id {
                        let new_owner = stream
                            .tree
                            .user_ids()
                            .into_iter()
                            .find(|uid| stream.tree.get_parent(*uid).ok().flatten().is_none());
                        match new_owner {
                            Some(nid) => {
                                stream.owner_id = nid;
                                self.stream_owners.entry(nid).or_default().push(oid);
                            }
                            None => {
                                stream.owner_id = Uuid::nil();
                            }
                        }
                    }
                }
            }
        }

        Ok(RemoveMemberResult { removed_from })
    }

    /// Returns summaries of all streams, sorted by created_at.
    pub fn list_streams(&self) -> Vec<StreamSummary> {
        let mut summaries: Vec<StreamSummary> = self
            .streams
            .values()
            .map(|s| StreamSummary {
                id: s.id,
                owner_id: s.owner_id,
                member_count: s.tree.user_ids().len(),
                frozen: s.frozen,
                created_at: s.created_at,
            })
            .collect();
        summaries.sort_by_key(|s| s.created_at);
        summaries
    }

    /// Returns info about a member's positions across all streams.
    pub fn get_member_info(&self, user_id: Uuid) -> Result<MemberInfo, StreamlineError> {
        let stream_ids = self
            .user_streams
            .get(&user_id)
            .ok_or(StreamlineError::MemberNotFound(user_id))?;

        let mut positions = Vec::new();
        for &stream_id in stream_ids {
            if let Some(stream) = self.streams.get(&stream_id) {
                if let Ok(pos) = stream.tree.get_position(user_id) {
                    positions.push(StreamPosition {
                        stream_id,
                        position: pos.depth as usize,
                        frozen: stream.frozen,
                    });
                }
            }
        }

        Ok(MemberInfo { streams: positions })
    }

    /// Adds a member to the engine.
    ///
    /// Bootstrap: the first member auto-creates stream 1 and becomes
    /// both the root and the owner. Subsequent members are appended to
    /// the bottom of the target stream's linear chain.
    pub fn add_member(
        &mut self,
        user_id: Uuid,
        sponsor_id: Uuid,
        timestamp: i64,
        stream_id_override: Option<u32>,
    ) -> Result<AddMemberResult, StreamlineError> {
        if self.user_streams.contains_key(&user_id) {
            return Err(StreamlineError::MemberAlreadyExists(user_id));
        }

        // Bootstrap: first member in the engine.
        if self.user_streams.is_empty() {
            return self.bootstrap_first_member(user_id, sponsor_id, timestamp);
        }

        // Validate sponsor exists in the engine.
        if !self.user_streams.contains_key(&sponsor_id)
            && !self.stream_owners.contains_key(&sponsor_id)
        {
            return Err(StreamlineError::SponsorNotFound(sponsor_id));
        }

        let target_stream_id = self.find_placement_stream(sponsor_id, stream_id_override)?;

        let stream = self
            .streams
            .get_mut(&target_stream_id)
            .expect("find_placement_stream returned a valid stream ID");

        // Append to the bottom of the chain.
        let position = match stream.bottom {
            Some(bottom_id) => {
                // A restored engine can name a bottom or a sponsor this tree
                // does not hold. Report what add_node observed rather than
                // asserting a condition this line never checked.
                stream
                    .tree
                    .add_node(user_id, bottom_id, sponsor_id, timestamp)
                    .map_err(|e| StreamlineError::TreeError(e.to_string()))?;
                let parent_depth = stream
                    .tree
                    .get_parent(user_id)
                    .expect("just added")
                    .expect("has parent")
                    .depth;
                parent_depth as usize + 1
            }
            None => {
                // Stream is empty (created by expansion but never populated).
                stream
                    .tree
                    .add_root(user_id, timestamp)
                    .expect("empty tree has no root");
                0
            }
        };

        stream.bottom = Some(user_id);
        self.user_streams
            .entry(user_id)
            .or_default()
            .push(target_stream_id);

        Ok(AddMemberResult {
            stream_id: target_stream_id,
            position,
        })
    }

    /// Bootstrap the first member into stream 1.
    fn bootstrap_first_member(
        &mut self,
        user_id: Uuid,
        _sponsor_id: Uuid,
        timestamp: i64,
    ) -> Result<AddMemberResult, StreamlineError> {
        let stream = self.streams.get_mut(&1).expect("stream 1 exists");
        stream
            .tree
            .add_root(user_id, timestamp)
            .expect("empty tree has no root");
        stream.owner_id = user_id;
        stream.bottom = Some(user_id);

        self.user_streams.entry(user_id).or_default().push(1);
        self.stream_owners.entry(user_id).or_default().push(1);

        Ok(AddMemberResult {
            stream_id: 1,
            position: 0,
        })
    }

    /// Expands a user's stream count to match their rank allowance.
    ///
    /// Go computes the total allowed count from rank config and passes
    /// it here. The engine is rank-agnostic.
    pub fn expand_streams(
        &mut self,
        user_id: Uuid,
        total_allowed: u32,
        timestamp: i64,
    ) -> Result<ExpansionResult, StreamlineError> {
        if !self.user_streams.contains_key(&user_id) && !self.stream_owners.contains_key(&user_id) {
            return Err(StreamlineError::MemberNotFound(user_id));
        }

        let current_count = self
            .stream_owners
            .get(&user_id)
            .map(|v| v.len() as u32)
            .unwrap_or(0);

        if total_allowed <= current_count {
            return Ok(ExpansionResult {
                new_stream_ids: Vec::new(),
            });
        }

        let to_create = total_allowed - current_count;
        let mut new_ids = Vec::with_capacity(to_create as usize);
        for _ in 0..to_create {
            let id = self.create_stream(user_id, timestamp);
            new_ids.push(id);
        }

        Ok(ExpansionResult {
            new_stream_ids: new_ids,
        })
    }

    /// Updates a user's active stream count based on rank changes.
    ///
    /// When `freeze_on_demotion` is true, excess streams are frozen
    /// (newest first). When false, excess streams are destroyed.
    /// Unfreezes or creates streams to increase the count.
    pub fn update_stream_allowance(
        &mut self,
        user_id: Uuid,
        total_allowed: u32,
        timestamp: i64,
    ) -> Result<FreezeResult, StreamlineError> {
        let owned_ids = self
            .stream_owners
            .get(&user_id)
            .ok_or(StreamlineError::NoOwnedStreams(user_id))?
            .clone();

        // Sort by created_at for deterministic ordering.
        let mut sorted: Vec<(u32, i64, bool)> = owned_ids
            .iter()
            .filter_map(|&id| {
                self.streams
                    .get(&id)
                    .map(|s| (s.id, s.created_at, s.frozen))
            })
            .collect();
        sorted.sort_by_key(|&(_, created_at, _)| created_at);

        let active_count = sorted.iter().filter(|&&(_, _, frozen)| !frozen).count() as u32;

        let mut result = FreezeResult {
            frozen: Vec::new(),
            unfrozen: Vec::new(),
            created: Vec::new(),
            destroyed: Vec::new(),
        };

        if total_allowed < active_count {
            let to_remove = active_count - total_allowed;
            let mut unfrozen_ids: Vec<u32> = sorted
                .iter()
                .filter(|&&(_, _, frozen)| !frozen)
                .map(|&(id, _, _)| id)
                .collect();
            unfrozen_ids.reverse(); // newest first

            if self.config.freeze_on_demotion {
                for &id in unfrozen_ids.iter().take(to_remove as usize) {
                    if let Some(stream) = self.streams.get_mut(&id) {
                        stream.frozen = true;
                        result.frozen.push(id);
                    }
                }
            } else {
                // Destroy excess streams. Remove members from user_streams.
                for &id in unfrozen_ids.iter().take(to_remove as usize) {
                    if let Some(stream) = self.streams.remove(&id) {
                        for member_id in stream.tree.user_ids() {
                            if let Some(streams) = self.user_streams.get_mut(&member_id) {
                                streams.retain(|&sid| sid != id);
                                if streams.is_empty() {
                                    self.user_streams.remove(&member_id);
                                }
                            }
                        }
                        result.destroyed.push(id);
                    }
                    // Remove from owner's list. Clean up empty entries.
                    if let Some(owned) = self.stream_owners.get_mut(&user_id) {
                        owned.retain(|&sid| sid != id);
                    }
                }
                if self
                    .stream_owners
                    .get(&user_id)
                    .is_some_and(|v| v.is_empty())
                {
                    self.stream_owners.remove(&user_id);
                }
            }
        } else if total_allowed > active_count {
            let deficit = total_allowed - active_count;

            let frozen_ids: Vec<u32> = sorted
                .iter()
                .filter(|&&(_, _, frozen)| frozen)
                .map(|&(id, _, _)| id)
                .collect();

            let mut remaining = deficit;
            for &id in &frozen_ids {
                if remaining == 0 {
                    break;
                }
                if let Some(stream) = self.streams.get_mut(&id) {
                    stream.frozen = false;
                    result.unfrozen.push(id);
                    remaining -= 1;
                }
            }

            for _ in 0..remaining {
                let id = self.create_stream(user_id, timestamp);
                result.created.push(id);
            }
        }

        Ok(result)
    }

    /// Creates a new empty stream owned by the given user.
    pub(crate) fn create_stream(&mut self, owner_id: Uuid, timestamp: i64) -> u32 {
        let id = self.next_stream_id;
        self.next_stream_id += 1;
        let stream = Stream {
            id,
            tree: UnilevelTree::new(),
            owner_id,
            frozen: false,
            created_at: timestamp,
            bottom: None,
        };
        self.streams.insert(id, stream);
        self.stream_owners.entry(owner_id).or_default().push(id);
        id
    }

    /// Determines which stream a new member should be placed in.
    fn find_placement_stream(
        &self,
        sponsor_id: Uuid,
        stream_id_override: Option<u32>,
    ) -> Result<u32, StreamlineError> {
        if let Some(override_id) = stream_id_override {
            // Reject explicit choice when config disables it.
            if !self.config.enrollment_stream_choice {
                return Err(StreamlineError::StreamChoiceNotAllowed);
            }
            // Explicit stream choice. Validate sponsor owns it and it's not frozen.
            let stream = self
                .streams
                .get(&override_id)
                .ok_or(StreamlineError::StreamNotFound(override_id))?;
            if stream.frozen {
                return Err(StreamlineError::StreamFrozen(override_id));
            }
            let owned = self.stream_owners.get(&sponsor_id).ok_or(
                StreamlineError::SponsorDoesNotOwnStream(sponsor_id, override_id),
            )?;
            if !owned.contains(&override_id) {
                return Err(StreamlineError::SponsorDoesNotOwnStream(
                    sponsor_id,
                    override_id,
                ));
            }
            return Ok(override_id);
        }

        let owned = self
            .stream_owners
            .get(&sponsor_id)
            .ok_or(StreamlineError::NoOwnedStreams(sponsor_id))?;

        match self.config.assignment_mode {
            StreamAssignmentMode::SponsorStream => {
                // First unfrozen owned stream.
                for &sid in owned {
                    if let Some(stream) = self.streams.get(&sid) {
                        if !stream.frozen {
                            return Ok(sid);
                        }
                    }
                }
                Err(StreamlineError::NoStreamsAvailable)
            }
            StreamAssignmentMode::RoundRobin => {
                // Unfrozen owned stream with fewest members.
                let mut best: Option<(u32, usize)> = None;
                for &sid in owned {
                    if let Some(stream) = self.streams.get(&sid) {
                        if stream.frozen {
                            continue;
                        }
                        let count = stream.tree.user_ids().len();
                        match best {
                            None => best = Some((sid, count)),
                            Some((_, best_count)) if count < best_count => {
                                best = Some((sid, count));
                            }
                            _ => {}
                        }
                    }
                }
                best.map(|(sid, _)| sid)
                    .ok_or(StreamlineError::NoStreamsAvailable)
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn default_config() -> StreamlineConfig {
        StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: false,
            freeze_on_demotion: true,
        }
    }

    fn choice_config() -> StreamlineConfig {
        StreamlineConfig {
            assignment_mode: StreamAssignmentMode::SponsorStream,
            enrollment_stream_choice: true,
            freeze_on_demotion: true,
        }
    }

    fn round_robin_config() -> StreamlineConfig {
        StreamlineConfig {
            assignment_mode: StreamAssignmentMode::RoundRobin,
            enrollment_stream_choice: false,
            freeze_on_demotion: true,
        }
    }

    fn test_uuid(n: u128) -> Uuid {
        Uuid::from_u128(n)
    }

    fn seeded_streamline() -> StreamlineEngine {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine
    }

    #[test]
    fn validate_restored_accepts_a_healthy_engine() {
        assert_eq!(seeded_streamline().validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_rejects_a_cursor_that_would_reallocate_a_live_stream() {
        // create_stream inserts without an occupancy check, so a cursor at or
        // below a live key replaces that stream and drops its whole tree.
        let mut engine = seeded_streamline();
        engine.next_stream_id = 1;

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamIdCursorNotPastEnd {
                next_stream_id: 1,
                highest_stream_id: 1,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_cursor_with_no_room_to_advance() {
        // create_stream assigns the cursor and then increments it, so this
        // value overflows on the next allocation.
        let mut engine = seeded_streamline();
        engine.next_stream_id = u32::MAX;

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamIdCursorExhausted {
                next_stream_id: u32::MAX,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_an_exhausted_cursor_on_an_engine_holding_no_streams() {
        let mut engine = seeded_streamline();
        engine.streams.clear();
        engine.user_streams.clear();
        engine.stream_owners.clear();
        engine.next_stream_id = u32::MAX;

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamIdCursorExhausted {
                next_stream_id: u32::MAX,
            })
        );
    }

    #[test]
    fn validate_restored_accepts_the_last_cursor_that_can_still_advance() {
        let mut engine = seeded_streamline();
        engine.next_stream_id = u32::MAX - 1;

        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_accepts_a_cursor_one_past_the_highest_stream() {
        let engine = seeded_streamline();
        assert_eq!(engine.next_stream_id, 2);
        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_accepts_a_cursor_on_an_engine_holding_no_streams() {
        let mut engine = seeded_streamline();
        engine.streams.clear();
        engine.user_streams.clear();
        engine.stream_owners.clear();

        assert_eq!(engine.validate_restored(), Ok(()));
    }

    #[test]
    fn add_member_reports_a_sponsor_the_target_tree_does_not_hold() {
        // A stream owner sits in the stream they were enrolled in, not the one
        // they own, so the owner is resolvable engine-wide and absent from this
        // tree. Reaching add_node with them as sponsor must not panic.
        let mut engine = seeded_streamline();
        let outsider = test_uuid(50);
        let second = engine.create_stream(outsider, 1001);
        engine
            .streams
            .get_mut(&second)
            .expect("just created")
            .tree
            .add_root(test_uuid(60), 1002)
            .expect("empty tree has no root");
        engine
            .streams
            .get_mut(&second)
            .expect("just created")
            .bottom = Some(test_uuid(60));
        engine
            .user_streams
            .entry(test_uuid(60))
            .or_default()
            .push(second);
        engine.user_streams.entry(outsider).or_default().push(1);

        let result = engine.add_member(test_uuid(70), outsider, 1003, None);

        assert!(
            matches!(result, Err(StreamlineError::TreeError(_))),
            "expected a coded rejection, got {result:?}"
        );
    }

    #[test]
    fn validate_restored_rejects_a_stream_filed_under_the_wrong_key() {
        // A mismatch between the map key and the stream's own id makes any
        // lookup by the other value silently miss.
        let mut engine = seeded_streamline();
        let stream = engine.streams.remove(&1).unwrap();
        engine.streams.insert(5, stream);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamIdMismatch {
                key: 5,
                stream_id: 1,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_bottom_absent_from_its_tree() {
        let mut engine = seeded_streamline();
        engine.streams.get_mut(&1).unwrap().bottom = Some(test_uuid(9));

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamBottomNotInTree {
                stream_id: 1,
                user_id: test_uuid(9),
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_missing_bottom_on_a_populated_tree() {
        let mut engine = seeded_streamline();
        engine.streams.get_mut(&1).unwrap().bottom = None;

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamBottomMissing {
                stream_id: 1,
                node_count: 1,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_tree_member_with_no_index_entry() {
        // The reverse of the user_streams walk. contains_member reads the
        // index alone, so an unindexed member is invisible to it.
        let mut engine = seeded_streamline();
        engine.user_streams.remove(&test_uuid(1));

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::TreeUserNotIndexed {
                stream_id: 1,
                user_id: test_uuid(1),
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_stream_offender_every_run() {
        for _ in 0..64 {
            let mut engine = seeded_streamline();
            let base = engine.streams.get(&1).unwrap();
            let (owner, created) = (base.owner_id, base.created_at);
            for key in [8u32, 9u32] {
                let mut s = StreamlineEngine::new(default_config(), created);
                let mut stream = s.streams.remove(&1).unwrap();
                stream.id = key;
                stream.owner_id = owner;
                stream.bottom = Some(test_uuid(9));
                engine.streams.insert(key, stream);
            }

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::StreamBottomNotInTree {
                    stream_id: 8,
                    user_id: test_uuid(9),
                }),
                "the lowest stream key should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_names_the_same_user_stream_offender_every_run() {
        for _ in 0..64 {
            let mut engine = seeded_streamline();
            engine.user_streams.insert(test_uuid(3), vec![77]);
            engine.user_streams.insert(test_uuid(2), vec![77]);

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::StreamAbsent {
                    user_id: test_uuid(2),
                    stream_id: 77,
                }),
                "the lowest user_id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_names_the_same_owner_offender_every_run() {
        for _ in 0..64 {
            let mut engine = seeded_streamline();
            for n in [3u128, 2u128] {
                engine.stream_owners.insert(test_uuid(n), vec![77]);
            }

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::OwnerStreamAbsent {
                    user_id: test_uuid(2),
                    stream_id: 77,
                }),
                "the lowest user_id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_an_empty_user_streams_entry() {
        // contains_member reads the index alone, so an empty entry made it
        // answer true for a user no tree holds, and add_member then refused
        // that user permanently.
        let mut engine = seeded_streamline();
        engine.user_streams.insert(test_uuid(5), vec![]);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::UserStreamsEntryEmpty {
                user_id: test_uuid(5),
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_unindexed_member_every_run() {
        // Two unindexed members in ONE stream. The hold keys on the pair, so
        // the tree's hash order cannot decide which is named.
        for _ in 0..64 {
            let mut engine = StreamlineEngine::new(default_config(), 1000);
            engine
                .add_member(test_uuid(1), test_uuid(99), 1000, None)
                .unwrap();
            engine
                .add_member(test_uuid(2), test_uuid(1), 1001, None)
                .unwrap();
            engine.user_streams.remove(&test_uuid(1));
            engine.user_streams.remove(&test_uuid(2));

            assert_eq!(
                engine.validate_restored(),
                Err(SnapshotConsistencyError::TreeUserNotIndexed {
                    stream_id: 1,
                    user_id: test_uuid(1),
                }),
                "the lowest (stream, user) pair should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_a_user_stream_that_is_absent() {
        let mut engine = seeded_streamline();
        engine.user_streams.insert(test_uuid(2), vec![77]);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamAbsent {
                user_id: test_uuid(2),
                stream_id: 77,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_user_absent_from_the_stream_tree() {
        // The state HEU-706 was filed for.
        let mut engine = seeded_streamline();
        engine.user_streams.insert(test_uuid(2), vec![1]);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::UserNotInStreamTree {
                user_id: test_uuid(2),
                stream_id: 1,
            })
        );
    }

    #[test]
    fn validate_restored_tells_an_absent_owner_stream_from_an_absent_user_stream() {
        // The two variants differ only in which map the message names, so
        // assert the variant rather than that it errored.
        let mut engine = seeded_streamline();
        engine.stream_owners.insert(test_uuid(2), vec![77]);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::OwnerStreamAbsent {
                user_id: test_uuid(2),
                stream_id: 77,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_an_owner_the_stream_does_not_name() {
        let mut engine = seeded_streamline();
        // uuid(2) is left out of user_streams entirely, so the owner walk is
        // what fires.
        engine.stream_owners.insert(test_uuid(2), vec![1]);

        assert_eq!(
            engine.validate_restored(),
            Err(SnapshotConsistencyError::StreamOwnerMismatch {
                user_id: test_uuid(2),
                stream_id: 1,
                owner_id: test_uuid(1),
            })
        );
    }

    #[test]
    fn validate_restored_wraps_a_nested_tree_fault_with_its_stream_id() {
        // Injected through serde, the way a real restore produces it, rather
        // than by reaching into state this module cannot touch.
        let engine = seeded_streamline();
        let mut json = serde_json::to_value(&engine).unwrap();
        json["streams"]["1"]["tree"]["arena"]["index"]
            .as_object_mut()
            .unwrap()
            .insert(test_uuid(5).to_string(), serde_json::json!(99));
        let tampered: StreamlineEngine = serde_json::from_value(json).unwrap();

        assert_eq!(
            tampered.validate_restored(),
            Err(SnapshotConsistencyError::Stream {
                stream_id: 1,
                source: Box::new(SnapshotConsistencyError::IndexSlotOutOfRange {
                    user_id: test_uuid(5),
                    slot: 99,
                    node_count: 1,
                }),
            })
        );
    }

    #[test]
    fn bootstrap_first_member() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        let result = engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        assert_eq!(result.stream_id, 1);
        assert_eq!(result.position, 0);
        assert!(engine.contains_member(test_uuid(1)));
        // First member owns stream 1.
        let stream = engine.get_stream(1).unwrap();
        assert_eq!(stream.owner_id, test_uuid(1));
    }

    #[test]
    fn add_second_member_sponsor_stream() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        let result = engine
            .add_member(test_uuid(2), test_uuid(1), 1001, None)
            .unwrap();
        assert_eq!(result.stream_id, 1);
        assert_eq!(result.position, 1);
    }

    #[test]
    fn add_member_round_robin() {
        let mut engine = StreamlineEngine::new(round_robin_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();

        // Give the sponsor a second stream.
        engine.create_stream(test_uuid(1), 1001);

        // First enrollee goes to stream with fewer members. Stream 1
        // has 1 member (the owner), stream 2 has 0.
        let r1 = engine
            .add_member(test_uuid(2), test_uuid(1), 1002, None)
            .unwrap();
        assert_eq!(r1.stream_id, 2);

        // Now both streams have 1 member. Next goes to lowest ID (stable sort).
        let r2 = engine
            .add_member(test_uuid(3), test_uuid(1), 1003, None)
            .unwrap();
        // Stream 1 has 1 member, stream 2 has 1 member. First match wins.
        assert_eq!(r2.stream_id, 1);
    }

    #[test]
    fn add_member_explicit_stream_override() {
        let mut engine = StreamlineEngine::new(choice_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.create_stream(test_uuid(1), 1001);

        let result = engine
            .add_member(test_uuid(2), test_uuid(1), 1002, Some(2))
            .unwrap();
        assert_eq!(result.stream_id, 2);
    }

    #[test]
    fn reject_duplicate_member() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        let err = engine
            .add_member(test_uuid(1), test_uuid(99), 1001, None)
            .unwrap_err();
        assert_eq!(err, StreamlineError::MemberAlreadyExists(test_uuid(1)));
    }

    #[test]
    fn reject_frozen_stream_override() {
        let mut engine = StreamlineEngine::new(choice_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.create_stream(test_uuid(1), 1001);

        // Freeze stream 2.
        engine.streams.get_mut(&2).unwrap().frozen = true;

        let err = engine
            .add_member(test_uuid(2), test_uuid(1), 1002, Some(2))
            .unwrap_err();
        assert_eq!(err, StreamlineError::StreamFrozen(2));
    }

    #[test]
    fn reject_nonexistent_sponsor() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        let err = engine
            .add_member(test_uuid(2), test_uuid(50), 1001, None)
            .unwrap_err();
        assert_eq!(err, StreamlineError::SponsorNotFound(test_uuid(50)));
    }

    // --- Task 3: expand_streams and update_stream_allowance ---

    #[test]
    fn expand_streams_creates_new() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        // User has 1 stream, expand to 3.
        let result = engine.expand_streams(test_uuid(1), 3, 1001).unwrap();
        assert_eq!(result.new_stream_ids.len(), 2);
        assert_eq!(engine.stream_count(), 3);
        // New streams are owned by user 1.
        for &id in &result.new_stream_ids {
            let stream = engine.get_stream(id).unwrap();
            assert_eq!(stream.owner_id, test_uuid(1));
            assert!(!stream.frozen);
        }
    }

    #[test]
    fn expand_streams_noop_at_limit() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        let result = engine.expand_streams(test_uuid(1), 1, 1001).unwrap();
        assert!(result.new_stream_ids.is_empty());
        assert_eq!(engine.stream_count(), 1);
    }

    #[test]
    fn freeze_excess_streams_newest_first() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        // Expand to 4 streams.
        engine.expand_streams(test_uuid(1), 4, 1001).unwrap();
        assert_eq!(engine.stream_count(), 4);

        // Drop allowance to 2. Streams 3 and 4 should freeze (newest first).
        let result = engine
            .update_stream_allowance(test_uuid(1), 2, 2000)
            .unwrap();
        assert_eq!(result.frozen.len(), 2);
        // Newest first means stream 4 frozen before stream 3.
        assert!(result.frozen.contains(&4));
        assert!(result.frozen.contains(&3));
        assert!(engine.get_stream(3).unwrap().frozen);
        assert!(engine.get_stream(4).unwrap().frozen);
        assert!(!engine.get_stream(1).unwrap().frozen);
        assert!(!engine.get_stream(2).unwrap().frozen);
    }

    #[test]
    fn unfreeze_oldest_first() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.expand_streams(test_uuid(1), 4, 1001).unwrap();
        // Freeze down to 1.
        engine
            .update_stream_allowance(test_uuid(1), 1, 2000)
            .unwrap();
        // All 3 extra streams frozen.
        assert!(engine.get_stream(2).unwrap().frozen);
        assert!(engine.get_stream(3).unwrap().frozen);
        assert!(engine.get_stream(4).unwrap().frozen);

        // Unfreeze back to 3. Oldest frozen first = stream 2, then 3.
        let result = engine
            .update_stream_allowance(test_uuid(1), 3, 2000)
            .unwrap();
        assert_eq!(result.unfrozen.len(), 2);
        assert!(!engine.get_stream(2).unwrap().frozen);
        assert!(!engine.get_stream(3).unwrap().frozen);
        assert!(engine.get_stream(4).unwrap().frozen);
    }

    #[test]
    fn freeze_then_unfreeze_round_trip() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.expand_streams(test_uuid(1), 3, 1001).unwrap();

        // Freeze to 1, then back to 3.
        engine
            .update_stream_allowance(test_uuid(1), 1, 2000)
            .unwrap();
        let result = engine
            .update_stream_allowance(test_uuid(1), 3, 2000)
            .unwrap();
        assert_eq!(result.unfrozen.len(), 2);
        // All streams should be active again.
        for id in 1..=3 {
            assert!(!engine.get_stream(id).unwrap().frozen);
        }
    }

    #[test]
    fn frozen_stream_rejects_placement() {
        let mut engine = StreamlineEngine::new(choice_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.expand_streams(test_uuid(1), 2, 1001).unwrap();

        // Freeze stream 2.
        engine
            .update_stream_allowance(test_uuid(1), 1, 2000)
            .unwrap();

        // Try to place in frozen stream 2 via override.
        let err = engine
            .add_member(test_uuid(2), test_uuid(1), 1002, Some(2))
            .unwrap_err();
        assert_eq!(err, StreamlineError::StreamFrozen(2));
    }

    // --- Task 4: remove_member and query methods ---

    #[test]
    fn remove_member_from_single_stream() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine
            .add_member(test_uuid(2), test_uuid(1), 1001, None)
            .unwrap();
        engine
            .add_member(test_uuid(3), test_uuid(1), 1002, None)
            .unwrap();
        // Chain: 1 → 2 → 3. Remove middle node (2).
        let result = engine.remove_member(test_uuid(2), 1003).unwrap();
        assert_eq!(result.removed_from, vec![1]);
        assert!(!engine.contains_member(test_uuid(2)));
        // Chain should be 1 → 3 after compaction.
        assert!(engine.contains_member(test_uuid(1)));
        assert!(engine.contains_member(test_uuid(3)));
        let stream = engine.get_stream(1).unwrap();
        assert_eq!(stream.tree.user_ids().len(), 2);
    }

    #[test]
    fn remove_member_from_multiple_streams() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.create_stream(test_uuid(1), 1001);

        // Place user 2 in stream 1 via sponsor.
        engine
            .add_member(test_uuid(2), test_uuid(1), 1002, None)
            .unwrap();
        // Manually add user 2 to stream 2 as well (simulate multi-stream position).
        let stream2 = engine.streams.get_mut(&2).unwrap();
        stream2.tree.add_root(test_uuid(2), 1002).unwrap();
        stream2.bottom = Some(test_uuid(2));
        engine.user_streams.get_mut(&test_uuid(2)).unwrap().push(2);

        let result = engine.remove_member(test_uuid(2), 1003).unwrap();
        assert_eq!(result.removed_from.len(), 2);
        assert!(!engine.contains_member(test_uuid(2)));
    }

    #[test]
    fn remove_nonexistent_member_errors() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        let err = engine.remove_member(test_uuid(50), 1001).unwrap_err();
        assert_eq!(err, StreamlineError::MemberNotFound(test_uuid(50)));
    }

    #[test]
    fn remove_owner_transfers_empty_stream_ownership() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        // Expand: user 1 now owns streams 1, 2, 3. Streams 2 and 3 are empty.
        engine.expand_streams(test_uuid(1), 3, 1001).unwrap();
        // Add a second member to stream 1 so it's not empty after removal.
        engine
            .add_member(test_uuid(2), test_uuid(1), 1002, None)
            .unwrap();

        // Remove user 1 (stream owner).
        engine.remove_member(test_uuid(1), 1003).unwrap();

        // Stream 1 should transfer ownership to user 2.
        let s1 = engine.get_stream(1).unwrap();
        assert_eq!(s1.owner_id, test_uuid(2));
        // Empty streams 2 and 3 should have nil owner (no members to transfer to).
        let s2 = engine.get_stream(2).unwrap();
        assert!(s2.owner_id.is_nil());
        let s3 = engine.get_stream(3).unwrap();
        assert!(s3.owner_id.is_nil());
    }

    #[test]
    fn list_streams_returns_sorted_summaries() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine.expand_streams(test_uuid(1), 3, 1001).unwrap();

        let summaries = engine.list_streams();
        assert_eq!(summaries.len(), 3);
        // Sorted by created_at.
        assert!(summaries[0].created_at <= summaries[1].created_at);
        assert!(summaries[1].created_at <= summaries[2].created_at);
        assert_eq!(summaries[0].id, 1);
    }

    #[test]
    fn get_member_info_returns_all_positions() {
        let mut engine = StreamlineEngine::new(default_config(), 1000);
        engine
            .add_member(test_uuid(1), test_uuid(99), 1000, None)
            .unwrap();
        engine
            .add_member(test_uuid(2), test_uuid(1), 1001, None)
            .unwrap();

        let info = engine.get_member_info(test_uuid(2)).unwrap();
        assert_eq!(info.streams.len(), 1);
        assert_eq!(info.streams[0].stream_id, 1);
        assert_eq!(info.streams[0].position, 1);
    }
}
