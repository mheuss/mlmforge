//! Streamline engine — manages all streams for one streamline structure.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::config::streamline::StreamAssignmentMode;
use crate::tree::unilevel::UnilevelTree;

use super::error::StreamlineError;
use super::types::AddMemberResult;

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

    /// Returns true if the user has a position in any stream.
    pub fn contains_member(&self, user_id: Uuid) -> bool {
        self.user_streams.contains_key(&user_id)
    }

    /// Returns the stream IDs a member has positions on.
    pub fn get_member_streams(&self, user_id: Uuid) -> Option<&Vec<u32>> {
        self.user_streams.get(&user_id)
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
                stream
                    .tree
                    .add_node(user_id, bottom_id, sponsor_id, timestamp)
                    .expect("bottom node exists in tree");
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

    /// Creates a new empty stream owned by the given user.
    ///
    /// Used internally by expand_streams (Task 3). Also exposed for test setup.
    #[allow(dead_code)]
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
        let mut engine = StreamlineEngine::new(default_config(), 1000);
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
        let mut engine = StreamlineEngine::new(default_config(), 1000);
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
}
