package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agreementEngine is the surface the agreement cases drive.
type agreementEngine interface {
	CreateTree(ctx context.Context, structure, treeType string) error
	CreateMatrixTree(ctx context.Context, structure string, width int, spillover string) error
	AddRoot(ctx context.Context, structure, userID string, enrolledAt int64) error
	AddNode(ctx context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error
	AddNodeAt(ctx context.Context, structure, userID, parentID, sponsorID string, position int, enrolledAt int64) error
	RemoveNode(ctx context.Context, structure, userID string) ([]Responsored, error)
	CheckMutation(ctx context.Context, structure string, m Mutation) error
}

// agreementCase is one refused request, checked and then applied.
type agreementCase struct {
	name  string
	code  string
	setup func(*testing.T, agreementEngine)
	check Mutation
	apply func(agreementEngine) error
}

func agreementTree(treeType string) func(*testing.T, agreementEngine) {
	return func(t *testing.T, e agreementEngine) {
		if treeType == treeTypeMatrix {
			require.NoError(t, e.CreateMatrixTree(context.Background(), writerTree, 3, "breadth_first"))
			return
		}
		require.NoError(t, e.CreateTree(context.Background(), writerTree, treeType))
	}
}

func agreementRooted(treeType string) func(*testing.T, agreementEngine) {
	return func(t *testing.T, e agreementEngine) {
		agreementTree(treeType)(t, e)
		require.NoError(t, e.AddRoot(context.Background(), writerTree, writerRoot, 100))
	}
}

// agreementWithChild places writerChild under writerRoot, at slot 0 where the
// tree has slots.
func agreementWithChild(treeType string) func(*testing.T, agreementEngine) {
	return func(t *testing.T, e agreementEngine) {
		ctx := context.Background()
		agreementRooted(treeType)(t, e)
		switch treeType {
		case treeTypeMatrix:
			require.NoError(t, e.AddNodeAt(ctx, writerTree, writerChild, writerRoot, writerRoot, 0, 200))
		case treeTypeBinary:
			require.NoError(t, e.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, 200, WithPosition(0)))
		default:
			require.NoError(t, e.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, 200))
		}
	}
}

func addNodeCase(name, code string, setup func(*testing.T, agreementEngine),
	user, parent, sponsor string, opts ...AddNodeOption) agreementCase {
	return agreementCase{name: name, code: code, setup: setup,
		check: CheckAddNode(user, parent, sponsor, 300, opts...),
		apply: func(e agreementEngine) error {
			return e.AddNode(context.Background(), writerTree, user, parent, sponsor, 300, opts...)
		}}
}

func addRootCase(name, code string, setup func(*testing.T, agreementEngine), user string) agreementCase {
	return agreementCase{name: name, code: code, setup: setup,
		check: CheckAddRoot(user, 100),
		apply: func(e agreementEngine) error {
			return e.AddRoot(context.Background(), writerTree, user, 100)
		}}
}

func removeNodeCase(name, code string, setup func(*testing.T, agreementEngine), user string) agreementCase {
	return agreementCase{name: name, code: code, setup: setup,
		check: CheckRemoveNode(user),
		apply: func(e agreementEngine) error {
			_, err := e.RemoveNode(context.Background(), writerTree, user)
			return err
		}}
}

func agreementCases() []agreementCase {
	return []agreementCase{
		addNodeCase("unilevel add_node of a held user", engineCodeUserAlreadyExists,
			agreementWithChild(treeTypeUnilevel), writerChild, writerOther, writerAbsentSponsor),
		addNodeCase("unilevel add_node under an absent parent", engineCodeUserNotFound,
			agreementRooted(treeTypeUnilevel), writerOther, writerChild, writerAbsentSponsor),
		addNodeCase("unilevel add_node with an absent sponsor", "SPONSOR_NOT_FOUND",
			agreementRooted(treeTypeUnilevel), writerOther, writerRoot, writerAbsentSponsor),
		addRootCase("add_root with a root held", engineCodeRootAlreadyExists,
			agreementRooted(treeTypeUnilevel), writerOther),
		addRootCase("add_root of the held root", engineCodeRootAlreadyExists,
			agreementRooted(treeTypeUnilevel), writerRoot),
		removeNodeCase("remove_node of a user the tree does not hold", engineCodeUserNotFound,
			agreementRooted(treeTypeUnilevel), writerOther),
		removeNodeCase("remove_node of a unilevel parent", "HAS_CHILDREN",
			agreementWithChild(treeTypeUnilevel), writerRoot),
		removeNodeCase("remove_node of a binary parent", "HAS_CHILDREN",
			agreementWithChild(treeTypeBinary), writerRoot),
	}
}

func TestFakeWriterEngine_AnswersAsTheWorkerDoes(t *testing.T) {
	binary := findWorkerBinary(t)
	ctx := context.Background()
	for _, tc := range agreementCases() {
		t.Run(tc.name, func(t *testing.T) {
			require.NotEmpty(t, tc.code, "a case names the code the worker refuses with")
			client, err := NewEngineClient(ctx, binary)
			require.NoError(t, err)
			defer func() { _ = client.Stop() }()
			fake := newFakeWriterEngine()
			tc.setup(t, client)
			tc.setup(t, fake)

			workerCheck := client.CheckMutation(ctx, writerTree, tc.check)
			fakeCheck := fake.CheckMutation(ctx, writerTree, tc.check)
			workerApply := tc.apply(client)
			fakeApply := tc.apply(fake)

			require.Equal(t, tc.code, engineCodeOf(workerCheck), "the worker's check answer")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
			assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
		})
	}
}
